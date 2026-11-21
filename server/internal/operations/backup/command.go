package backup

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/identity"
)

const Usage = `personad backup create --output <file> --writes-quiesced [database options]
                           create and temporarily restore both databases before atomically publishing a mode-0600 whole-install bundle
  personad backup inspect --bundle <file> [--json]
                           check bundle integrity offline and print a summary
  personad backup verify --bundle <file> [database options]
                           verify every SHA-256 and restore both dumps into temporary databases (no service writes)
  personad backup restore --bundle <file> --writes-quiesced [--allow-external-kb] [database options]
                           stage both databases and runtime files, preserve the previous databases, and roll back on failure

  Put create's --output outside the knowledge base and the runtime's managers/assistant, extensions, skills,
  hub.machines_kept and tls directories: they are collected, so create refuses it (a case-insensitive file system may let a
  differently-cased path through, so spell the path as it is).
  Needs pg_dump/pg_restore of the server's major version on PATH, or (for a loopback server) docker with the
  persona-postgres container. The shipped Docker Compose deployment (database host "postgres", hub image without
  client tools or docker) is not supported yet and stops before any work.`

type operatorRunner interface {
	Create(context.Context, Options) (Result, error)
	Verify(context.Context, Options) (Result, error)
	Restore(context.Context, Options) (Result, error)
}

// Run is the top-level personad backup command.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "inspect" {
		return runInspect(ctx, args[1:], stdout)
	}
	return runWithOperator(ctx, NewOperator(), args, stdout, stderr)
}

func operationSecrets(opts Options) []string {
	secrets := []string{opts.DatabaseURL, opts.ManagerDatabaseURL, identity.Getenv("PERSONA_DATABASE_URL")}
	if hub, manager, err := Discover(opts); err == nil {
		secrets = append(secrets, targetSecrets(hub)...)
		secrets = append(secrets, targetSecrets(manager)...)
	}
	for _, name := range []string{filepath.Join(opts.Home, "hub.json"), filepath.Join(opts.Home, "managers", "assistant", "manager.json")} {
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) == nil {
			collectSensitiveStrings(value, "", &secrets)
		}
	}
	return secrets
}

func collectSensitiveStrings(value any, key string, out *[]string) {
	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			collectSensitiveStrings(child, childKey, out)
		}
	case []any:
		for _, child := range value {
			collectSensitiveStrings(child, key, out)
		}
	case string:
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		if normalized == "key" || normalized == "database_url" || normalized == "tailcat_addr" ||
			strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "password") || strings.Contains(normalized, "api_key") ||
			strings.Contains(normalized, "apikey") || strings.Contains(normalized, "credential") {
			*out = append(*out, value)
		}
	}
}

func runWithOperator(ctx context.Context, operator operatorRunner, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return clitext.UsageError("usage: " + Usage)
	}
	command := args[0]
	if command != "create" && command != "verify" && command != "restore" {
		return clitext.UsageError("unknown backup command " + fmt.Sprintf("%q", command) + "\nusage: " + Usage)
	}
	flags := flag.NewFlagSet("personad backup "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	opts := Options{}
	flags.StringVar(&opts.Home, "home", identity.HomeDir(), "Persona runtime directory")
	flags.StringVar(&opts.DatabaseURL, "database-url", "", "override the hub database URL (never printed)")
	flags.StringVar(&opts.ManagerDatabaseURL, "manager-database-url", "", "override the built-in manager database URL (never printed)")
	flags.StringVar(&opts.HubDockerContainer, "hub-docker-container", "", "Docker container that supplies hub pg_dump/pg_restore")
	flags.StringVar(&opts.ManagerDockerContainer, "manager-docker-container", "", "Docker container that supplies manager pg_dump/pg_restore")
	if command == "create" || command == "restore" {
		flags.BoolVar(&opts.WritesQuiesced, "writes-quiesced", false, "confirm the hub and built-in manager are stopped")
	}
	if command == "create" {
		flags.StringVar(&opts.Output, "output", "", "new bundle path; an existing file is never overwritten")
	} else {
		flags.StringVar(&opts.Bundle, "bundle", "", "bundle to read")
	}
	if command == "restore" {
		flags.BoolVar(&opts.AllowExternalKB, "allow-external-kb", false, "allow replacing the bundle's kb_dir outside the runtime directory")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return clitext.UsageError("usage: personad backup " + command + " [options]")
	}
	var result Result
	var err error
	switch command {
	case "create":
		result, err = operator.Create(ctx, opts)
	case "verify":
		result, err = operator.Verify(ctx, opts)
	case "restore":
		result, err = operator.Restore(ctx, opts)
	}
	if err != nil {
		return errors.New(redact(err.Error(), operationSecrets(opts)...))
	}
	printResult(stdout, command, result)
	return nil
}

func printResult(stdout io.Writer, command string, result Result) {
	switch {
	case result.Restored:
		fmt.Fprintf(stdout, "restored and verified %s\n", result.Bundle)
		if result.PreviousHubDB != "" {
			fmt.Fprintf(stdout, "previous hub database preserved as %s\n", result.PreviousHubDB)
		}
		if result.PreviousManagerDB != "" {
			fmt.Fprintf(stdout, "previous built-in manager database preserved as %s\n", result.PreviousManagerDB)
		}
		if result.PreviousFiles != "" {
			fmt.Fprintf(stdout, "previous runtime files preserved in %s\n", result.PreviousFiles)
		}
		if result.PreviousExternalKB != "" {
			fmt.Fprintf(stdout, "previous external knowledge base preserved in %s\n", result.PreviousExternalKB)
		}
		if result.KeptDockerPassword {
			fmt.Fprintln(stdout, "kept this install's docker/db-password: the bundle's copy belongs to the database cluster it was taken from, not to this one")
		}
	case result.Verified && command == "create":
		fmt.Fprintf(stdout, "created and verified %s\n", result.Bundle)
	default:
		fmt.Fprintf(stdout, "verified %s\n", result.Bundle)
	}
	for _, note := range result.Notes {
		fmt.Fprintln(stdout, note)
	}
}
