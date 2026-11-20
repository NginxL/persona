package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/secretkey"
)

type inspectionSize struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

func (s *inspectionSize) add(entry FileEntry) {
	s.Files++
	s.Bytes += entry.Size
}

type inspectionKnowledge struct {
	State    string `json:"state"`
	Location string `json:"location"`
	inspectionSize
}

// inspectionReport projects a verified manifest into shareable aggregate data.
// Paths, configuration, hashes, and encryption-key identities stay in the bundle.
type inspectionReport struct {
	Check                   string              `json:"check"`
	Format                  int                 `json:"format"`
	CreatedAt               time.Time           `json:"created_at"`
	ArchiveBytes            int64               `json:"archive_bytes"`
	HubDump                 inspectionSize      `json:"hub_dump"`
	ManagerDump             inspectionSize      `json:"manager_dump"`
	Runtime                 inspectionSize      `json:"runtime"`
	KnowledgeBase           inspectionKnowledge `json:"knowledge_base"`
	DatabaseRestoreVerified bool                `json:"database_restore_verified"`
}

func runInspect(ctx context.Context, args []string, stdout io.Writer) error {
	lang := i18n.Terminal()
	usage := i18n.T(lang, "backup-inspect.usage", i18n.Args{"command": clitext.Named("personad backup inspect")})
	flags := flag.NewFlagSet("backup inspect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	bundle := flags.String("bundle", "", "")
	asJSON := flags.Bool("json", false, "")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(stdout, usage)
		return err
	} else if err != nil || flags.NArg() != 0 || *bundle == "" {
		return clitext.UsageError(usage)
	}
	report, err := inspectBundle(ctx, *bundle)
	if err != nil {
		return describeInspectionError(lang, err)
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(report)
	}
	return printInspection(stdout, lang, report)
}

func describeInspectionError(lang i18n.Lang, err error) error {
	var issue *bundleProblem
	key := "backup-inspect.error_unreadable"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		key = "backup-inspect.error_canceled"
	case errors.As(err, &issue):
		// These categories are internal constants. Archive errors can contain
		// private member names, so their original text never reaches the CLI.
		key = "backup-inspect.error_" + string(issue.kind)
	}
	return errors.New(i18n.T(lang, key))
}

func inspectBundle(ctx context.Context, bundle string) (inspectionReport, error) {
	if err := ctx.Err(); err != nil {
		return inspectionReport{}, err
	}
	file, size, err := openBundle(bundle)
	if err != nil {
		return inspectionReport{}, err
	}
	defer file.Close()
	return inspectArchive(ctx, file, size)
}

func inspectArchive(ctx context.Context, source io.ReadSeeker, size int64) (inspectionReport, error) {
	// Keep one descriptor for both passes. Read the manifest, then rewind to
	// check all members against it, including any members preceding the manifest.
	read := contextReader{ctx: ctx, reader: source}
	manifest, err := findBundleManifest(read)
	if err != nil {
		return inspectionReport{}, err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return inspectionReport{}, problem(bundleUnreadable, err)
	}
	var lastEnd int64
	counted := &archiveCounter{Reader: read}
	err = walkBundle(counted, manifest, func(reader io.Reader, header *tar.Header) error {
		if header.Mode != 0o600 {
			return problem(bundleManifest, errors.New("the archive manifest must be mode 0600"))
		}
		if err := compareArchiveManifest(reader, header, manifest); err != nil {
			return err
		}
		lastEnd = counted.offset
		return nil
	}, func(reader io.Reader, entry FileEntry) error {
		if err := inspectMember(reader, entry, manifest); err != nil {
			return err
		}
		lastEnd = counted.offset
		return nil
	})
	if err != nil {
		return inspectionReport{}, err
	}
	if err := inspectEnding(read, size, lastEnd, counted.offset); err != nil {
		return inspectionReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return inspectionReport{}, err
	}
	return summarizeInspection(manifest, size), nil
}

func inspectMember(reader io.Reader, entry FileEntry, manifest *Manifest) error {
	var fingerprint string
	switch entry.Path {
	case hubKeyPath:
		fingerprint = manifest.HubKeyFingerprint
	case managerKeyPath:
		fingerprint = manifest.ManagerKeyFingerprint
	default:
		return checkBundleMember(reader, io.Discard, entry)
	}
	if entry.Size != secretkey.Size {
		return problem(bundleKey, errors.New("the bundled encryption key has the wrong size"))
	}
	var key [secretkey.Size]byte
	buf := bytes.NewBuffer(key[:0])
	defer clear(key[:])
	if err := checkBundleMember(reader, buf, entry); err != nil {
		return err
	}
	if secretkey.Fingerprint(buf.Bytes()) != fingerprint {
		return problem(bundleKey, errors.New("the bundled encryption key does not match its fingerprint"))
	}
	return nil
}

const tarBlock = 512

type archiveCounter struct {
	io.Reader
	offset int64
}

func (r *archiveCounter) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.offset += int64(n)
	return n, err
}

func inspectEnding(reader io.Reader, size, lastEnd, end int64) error {
	// archive/tar treats EOF after the last file (and even after just one
	// zero block) as a valid end. A published bundle has both zero blocks.
	padding := (tarBlock - lastEnd%tarBlock) % tarBlock
	if end-lastEnd != padding+2*tarBlock || size < end || (size-end)%tarBlock != 0 {
		return problem(bundleIncomplete, errors.New("the archive end is missing or incomplete"))
	}
	// Some tar tools add more zero records. Ignore those records, and refuse
	// a concatenated archive or other bytes following the first archive.
	var buffer [32 << 10]byte
	for remaining := size - end; remaining > 0; {
		n, err := io.ReadFull(reader, buffer[:min(int64(len(buffer)), remaining)])
		if err != nil {
			return problem(bundleIncomplete, err)
		}
		for _, b := range buffer[:n] {
			if b != 0 {
				return problem(bundleLayout, errors.New("the archive has trailing data"))
			}
		}
		remaining -= int64(n)
	}
	if n, err := reader.Read(buffer[:1]); n != 0 || !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("the bundle grew during inspection")
		}
		return problem(bundleIncomplete, err)
	}
	return nil
}

func summarizeInspection(manifest *Manifest, size int64) inspectionReport {
	result := inspectionReport{
		Check: "offline_integrity", Format: manifest.Format, CreatedAt: manifest.CreatedAt.UTC(), ArchiveBytes: size,
		KnowledgeBase: inspectionKnowledge{State: "absent", Location: "unspecified"},
	}
	switch {
	case manifest.KnowledgeBase.External != "":
		result.KnowledgeBase.Location = "external"
	case manifest.KnowledgeBase.Relative != "":
		result.KnowledgeBase.Location = "internal"
	}
	for _, entry := range manifest.Files {
		switch {
		case entry.Path == hubDumpPath:
			result.HubDump.add(entry)
		case entry.Path == managerDumpPath:
			result.ManagerDump.add(entry)
		case strings.HasPrefix(entry.Path, "kb/"):
			result.KnowledgeBase.add(entry)
		default:
			result.Runtime.add(entry)
		}
	}
	if manifest.KnowledgeBase.Present {
		result.KnowledgeBase.State = "empty"
		if result.KnowledgeBase.Files > 0 {
			result.KnowledgeBase.State = "included"
		}
	}
	return result
}

func printInspection(output io.Writer, lang i18n.Lang, report inspectionReport) error {
	var rendered strings.Builder
	fmt.Fprintln(&rendered, i18n.T(lang, "backup-inspect.pass"))
	fmt.Fprintln(&rendered, i18n.T(lang, "backup-inspect.archive", i18n.Args{
		"format": report.Format, "time": report.CreatedAt.Format(time.RFC3339), "bytes": report.ArchiveBytes,
	}))
	for _, part := range []struct {
		label string
		inspectionSize
	}{
		{"backup-inspect.hub", report.HubDump}, {"backup-inspect.manager", report.ManagerDump},
		{"backup-inspect.runtime", report.Runtime}, {"backup-inspect.knowledge_base", report.KnowledgeBase.inspectionSize},
	} {
		fmt.Fprintln(&rendered, i18n.T(lang, "backup-inspect.inventory", i18n.Args{
			"part": i18n.T(lang, part.label), "files": part.Files, "bytes": part.Bytes,
		}))
	}
	fmt.Fprintln(&rendered, i18n.T(lang, "backup-inspect.knowledge_state", i18n.Args{
		"state":    i18n.T(lang, "backup-inspect.knowledge_"+report.KnowledgeBase.State),
		"location": i18n.T(lang, "backup-inspect.location_"+report.KnowledgeBase.Location),
	}))
	fmt.Fprintln(&rendered, i18n.T(lang, "backup-inspect.verify_hint", i18n.Args{
		"command": clitext.Named("personad backup verify"),
	}))
	_, err := io.WriteString(output, rendered.String())
	return err
}
