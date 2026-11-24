package transcript

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// Bounded constants for deterministic transcript-based context category estimation.
const (
	// breakdownSampleLimit bounds per-string UTF-8 inspection so estimation stays O(1) per block.
	breakdownSampleLimit = 4096
	// imageTokenBytesEstimate approximates a single inline image (~1,600 tokens at 4 bytes/token)
	// rather than counting raw base64 payload bytes as text tokens.
	imageTokenBytesEstimate int64 = 6400
	// skippedLineEnvelopeBytes approximates JSON envelope overhead on large skipped lines.
	skippedLineEnvelopeBytes int64 = 256
	// maxSkippedLineTokens bounds JSON token stepping when scanning large skipped lines for base64 images.
	maxSkippedLineTokens = 8192
	// maxSkippedLineDepth bounds the fixed stack depth for skipped-line scanning.
	maxSkippedLineDepth = 64
)

// ContextStats accumulates bounded category byte weights from parsed transcript records since the
// latest compaction in the scanned window. Zero values represent no observed evidence.
type ContextStats struct {
	SystemBytes       int64
	ToolsBytes        int64
	ConversationBytes int64
	ToolOutputBytes   int64
	CompactedInWindow bool
	Native            *proto.ContextBreakdown
}

// Empty reports whether no transcript category bytes or native breakdown have been observed.
func (s ContextStats) Empty() bool {
	return s.SystemBytes == 0 && s.ToolsBytes == 0 && s.ConversationBytes == 0 && s.ToolOutputBytes == 0 && s.Native == nil
}

// ResetCompacted clears pre-compaction category accumulators and marks that compaction occurred
// within the current scan window.
func (s *ContextStats) ResetCompacted() {
	s.SystemBytes = 0
	s.ToolsBytes = 0
	s.ConversationBytes = 0
	s.ToolOutputBytes = 0
	s.CompactedInWindow = true
	s.Native = nil
}

// SetNative stores a sanitized engine-reported exact breakdown when valid.
func (s *ContextStats) SetNative(b *proto.ContextBreakdown) {
	if b == nil {
		return
	}
	cp := b.Clone()
	if cp.Source == "" {
		cp.Source = proto.ContextBreakdownExact
	}
	if san := cp.Sanitized(); san != nil {
		s.Native = san
	}
}

// ObserveSkippedUserLine accounts for a large user transcript line skipped by quickLine without
// full JSON decoding. When base64 image blocks are present, a bounded zero-allocation JSON scan
// strips only the base64 image source objects, counting each image block individually with
// imageTokenBytesEstimate while preserving the remaining non-image text payload.
func (m *Meta) ObserveSkippedUserLine(line []byte) {
	if len(line) == 0 || bytes.Contains(line, []byte(`"isSidechain":true`)) {
		return
	}
	isMeta := bytes.Contains(line, []byte(`"isMeta":true`)) || bytes.Contains(line, []byte(`"isCompactSummary":true`))
	isToolResult := bytes.Contains(line, []byte(`"tool_result"`))

	if !bytes.Contains(line, []byte(`"base64"`)) {
		payload := int64(len(line)) - skippedLineEnvelopeBytes
		if payload <= 0 {
			return
		}
		switch {
		case isToolResult:
			m.ContextStats.ToolOutputBytes += payload
		case isMeta:
			m.ContextStats.SystemBytes += payload
		default:
			m.ContextStats.ConversationBytes += payload
		}
		return
	}

	removedBytes, convImages, toolImages := scanSkippedBase64Images(line)
	totalImages := convImages + toolImages
	nonImageBytes := int64(len(line)) - removedBytes - skippedLineEnvelopeBytes - totalImages*26
	if nonImageBytes < 0 {
		nonImageBytes = 0
	}
	if isMeta {
		m.ContextStats.SystemBytes += nonImageBytes + totalImages*imageTokenBytesEstimate
		return
	}
	m.ContextStats.ToolOutputBytes += toolImages * imageTokenBytesEstimate
	m.ContextStats.ConversationBytes += convImages * imageTokenBytesEstimate
	if nonImageBytes > 0 {
		if isToolResult {
			m.ContextStats.ToolOutputBytes += nonImageBytes
		} else {
			m.ContextStats.ConversationBytes += nonImageBytes
		}
	}
}

type skippedFrame struct {
	isObj         bool
	expectKey     bool
	start         int
	pendingKey    []byte
	hasBase64     bool
	hasData       bool
	isToolResult  bool
	pendingImages int64
}

// fastJSONStringEnd finds the closing quote of the JSON string starting at line[start] == '"',
// using bytes.IndexByte for fast skipping over large base64 payloads, or -1 if unterminated.
func fastJSONStringEnd(line []byte, start int) int {
	i := start + 1
	for i < len(line) {
		rel := bytes.IndexByte(line[i:], '"')
		if rel < 0 {
			return -1
		}
		q := i + rel
		bs := 0
		for j := q - 1; j > start && line[j] == '\\'; j-- {
			bs++
		}
		if bs%2 == 0 {
			return q
		}
		i = q + 1
	}
	return -1
}

// scanSkippedBase64Images performs a bounded, zero-allocation JSON scan over a skipped user line
// to locate base64 image source objects ({"type":"base64",...,"data":"..."}), returning their
// combined raw JSON byte length and separate counts for conversation vs tool_result images.
func scanSkippedBase64Images(line []byte) (removedBytes, convImages, toolImages int64) {
	var stack [maxSkippedLineDepth]skippedFrame
	depth := 0
	extraDepth := 0
	tokens := 0
	hasToolAncestor := func(d int) bool {
		for j := d - 1; j >= 0; j-- {
			if stack[j].isToolResult {
				return true
			}
		}
		return false
	}

	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			tokens++
			if tokens > maxSkippedLineTokens {
				i = len(line)
				break
			}
			end := fastJSONStringEnd(line, i)
			if end < 0 {
				i = len(line)
				break
			}
			if extraDepth == 0 && depth > 0 && stack[depth-1].isObj {
				top := &stack[depth-1]
				if top.expectKey {
					top.pendingKey = line[i+1 : end]
					top.expectKey = false
				} else if len(top.pendingKey) > 0 {
					val := line[i+1 : end]
					switch string(top.pendingKey) {
					case "type":
						if bytes.Equal(val, []byte("base64")) {
							top.hasBase64 = true
						} else if bytes.Equal(val, []byte("tool_result")) {
							top.isToolResult = true
						}
					case "data":
						if len(val) > 0 {
							top.hasData = true
						}
					}
					top.pendingKey = nil
				}
			}
			i = end
		case '{':
			tokens++
			if tokens > maxSkippedLineTokens {
				i = len(line)
				break
			}
			if depth < maxSkippedLineDepth && extraDepth == 0 {
				if depth > 0 && stack[depth-1].isObj {
					stack[depth-1].pendingKey = nil
					stack[depth-1].expectKey = false
				}
				stack[depth] = skippedFrame{isObj: true, expectKey: true, start: i}
				depth++
			} else {
				extraDepth++
			}
		case '[':
			tokens++
			if tokens > maxSkippedLineTokens {
				i = len(line)
				break
			}
			if depth < maxSkippedLineDepth && extraDepth == 0 {
				if depth > 0 && stack[depth-1].isObj {
					stack[depth-1].pendingKey = nil
					stack[depth-1].expectKey = false
				}
				stack[depth] = skippedFrame{isObj: false}
				depth++
			} else {
				extraDepth++
			}
		case '}':
			if extraDepth > 0 {
				extraDepth--
				continue
			}
			if depth == 0 {
				continue
			}
			depth--
			fr := stack[depth]
			imgs := fr.pendingImages
			if fr.isObj && fr.hasBase64 && fr.hasData {
				removedBytes += int64(i - fr.start + 1)
				imgs++
			}
			if imgs > 0 {
				if fr.isToolResult || hasToolAncestor(depth) {
					toolImages += imgs
				} else if depth > 0 {
					stack[depth-1].pendingImages += imgs
				} else {
					convImages += imgs
				}
			}
		case ']':
			if extraDepth > 0 {
				extraDepth--
				continue
			}
			if depth == 0 {
				continue
			}
			depth--
			fr := stack[depth]
			if fr.pendingImages > 0 {
				if hasToolAncestor(depth) {
					toolImages += fr.pendingImages
				} else if depth > 0 {
					stack[depth-1].pendingImages += fr.pendingImages
				} else {
					convImages += fr.pendingImages
				}
			}
		case ',':
			if extraDepth == 0 && depth > 0 && stack[depth-1].isObj {
				stack[depth-1].expectKey = true
				stack[depth-1].pendingKey = nil
			}
		case ':':
			if extraDepth == 0 && depth > 0 && stack[depth-1].isObj {
				stack[depth-1].expectKey = false
			}
		}
	}

	// Flush any pending images if the scan terminated early (e.g., truncated JSON or token cap).
	inTool := false
	for j := 0; j < depth; j++ {
		if stack[j].isToolResult {
			inTool = true
		}
		if stack[j].pendingImages > 0 {
			if inTool {
				toolImages += stack[j].pendingImages
			} else {
				convImages += stack[j].pendingImages
			}
		}
	}
	return removedBytes, convImages, toolImages
}

// observeUserText classifies a user text block, separating injected system/context tags from user turns.
func (m *Meta) observeUserText(text string, isMeta bool) {
	if text == "" {
		return
	}
	w := estimateWeightedBytes(text)
	if isMeta || strings.Contains(text, "<system-reminder>") || strings.Contains(text, "<persona-context") {
		m.ContextStats.SystemBytes += w
		return
	}
	m.ContextStats.ConversationBytes += w
}

// observeToolContext accounts for unclipped tool name, raw JSON input, and raw tool output.
func (m *Meta) observeToolContext(name string, input json.RawMessage, output string) {
	if m == nil {
		return
	}
	m.ContextStats.ToolOutputBytes += int64(len(name)) + estimateWeightedBytes(string(input)) + estimateWeightedBytes(output)
}

// SampleContextBytes weighs a bounded UTF-8 sample without extrapolating to the full field size.
// Use it for exports that can contain opaque blobs or clipped previews. Long base64-like runs
// contribute no text weight, including when embedded in otherwise useful text or JSON.
func SampleContextBytes[T ~string | ~[]byte](text T) int64 {
	sample := strings.TrimSpace(breakdownSample(text))
	weight := weightContextSample(sample)
	const minOpaqueRun = 64 // also handle wrapped base64 output
	run := 0
	for i := range len(sample) {
		c := sample[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			run++
			continue
		}
		if run >= minOpaqueRun {
			weight -= int64(run)
		}
		run = 0
	}
	if run >= minOpaqueRun {
		weight -= int64(run)
	}
	return weight
}

// estimateWeightedBytes converts text into ASCII-equivalent bytes (~4 weighted bytes per token),
// scanning at most breakdownSampleLimit bytes so cost is strictly O(1) bounded.
func estimateWeightedBytes(s string) int64 {
	sample := breakdownSample(s)
	weight := weightContextSample(sample)
	if len(sample) == len(s) {
		return weight
	}
	if sample == "" {
		return int64(len(s))
	}
	return (weight * int64(len(s))) / int64(len(sample))
}

func breakdownSample[T ~string | ~[]byte](s T) string {
	sample := string(s[:min(len(s), breakdownSampleLimit)])
	if len(s) > len(sample) && sample[len(sample)-1] >= utf8.RuneSelf {
		// Trim a trailing incomplete UTF-8 rune split across the sample boundary in O(1):
		// walk back over at most 3 continuation bytes to the candidate rune start.
		start := len(sample) - 1
		for steps := 0; steps < 3 && (sample[start]&0xc0) == 0x80; steps++ {
			start--
		}
		if !utf8.FullRuneInString(sample[start:]) {
			sample = sample[:start]
		}
	}
	return sample
}

func weightContextSample(sample string) int64 {
	var asciiBytes, nonASCIIRunes int64
	for i := 0; i < len(sample); {
		if sample[i] < utf8.RuneSelf {
			asciiBytes++
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(sample[i:])
		nonASCIIRunes++
		i += size
	}
	// ASCII averages ~4 bytes/token (weight 1/byte); non-ASCII runes average ~1 token/rune (weight 4/rune).
	return asciiBytes + nonASCIIRunes*4
}

// EstimateTokens converts weighted bytes to a bounded, deterministic token count (~4 bytes/token).
func EstimateTokens(weightedBytes int64) int {
	if weightedBytes <= 0 {
		return 0
	}
	tokens := (weightedBytes + 3) / 4
	if tokens > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(tokens)
}

// BuildContextBreakdown constructs an honest proto.ContextBreakdown from observed transcript stats
// and real session context counts. It prefers native exact breakdowns, falls back to deterministic
// transcript-based estimation marked "estimated", and returns nil when no reliable data exists.
func BuildContextBreakdown(stats ContextStats, contextTokens, contextWindow, autoCompactThreshold int, fullWindow bool) *proto.ContextBreakdown {
	if contextWindow > 0 && autoCompactThreshold > contextWindow {
		autoCompactThreshold = contextWindow
	}
	if stats.Native != nil {
		if b := stats.Native.Sanitized(); b != nil {
			if b.Free == nil && contextWindow > 0 && contextTokens > 0 {
				b.Free = proto.Int(max(contextWindow-contextTokens, 0))
			}
			if b.AutoCompactThreshold == nil && autoCompactThreshold > 0 {
				b.AutoCompactThreshold = proto.Int(autoCompactThreshold)
			}
			return b
		}
	}
	if contextTokens <= 0 {
		return nil
	}
	estSys := EstimateTokens(stats.SystemBytes)
	estTools := EstimateTokens(stats.ToolsBytes)
	estConv := EstimateTokens(stats.ConversationBytes)
	estToolOut := EstimateTokens(stats.ToolOutputBytes)
	observed := estSys + estTools + estConv + estToolOut
	if observed <= 0 {
		return nil
	}

	out := &proto.ContextBreakdown{Source: proto.ContextBreakdownEstimated}
	if fullWindow && observed <= contextTokens {
		// All transcript turns since session start or last compaction were observed.
		// The unrecorded prefix (system prompt, rules, and engine-injected context) makes up the gap.
		sysTotal := estSys + (contextTokens - observed)
		if sysTotal > 0 || estSys > 0 {
			out.SystemAndRules = proto.Int(sysTotal)
		}
		if estTools > 0 {
			out.ToolsAndMCP = proto.Int(estTools)
		}
		out.Conversation = proto.Int(estConv)
		out.ToolOutputs = proto.Int(estToolOut)
	} else {
		// Either the byte heuristic exceeded actual contextTokens or we only scanned a bounded tail:
		// proportionally apportion actual contextTokens across the observed categories without
		// fabricating unobserved categories.
		type bucket struct {
			weight  int
			present bool
			alloc   int
			rem     int64
		}
		buckets := [4]bucket{
			{weight: estSys, present: estSys > 0},
			{weight: estTools, present: estTools > 0},
			{weight: estConv, present: estConv > 0 || fullWindow},
			{weight: estToolOut, present: estToolOut > 0 || fullWindow},
		}
		totalWeight := int64(observed)
		allocated := 0
		for i := range buckets {
			if buckets[i].weight <= 0 {
				continue
			}
			num := int64(buckets[i].weight) * int64(contextTokens)
			buckets[i].alloc = int(num / totalWeight)
			buckets[i].rem = num % totalWeight
			allocated += buckets[i].alloc
		}
		// Distribute rounding remainder deterministically by largest remainder (tie-break by index).
		left := contextTokens - allocated
		for left > 0 {
			best := -1
			for i := range buckets {
				if buckets[i].weight <= 0 {
					continue
				}
				if best < 0 || buckets[i].rem > buckets[best].rem {
					best = i
				}
			}
			if best < 0 {
				break
			}
			buckets[best].alloc++
			buckets[best].rem = -1
			left--
		}
		if buckets[0].present {
			out.SystemAndRules = proto.Int(buckets[0].alloc)
		}
		if buckets[1].present {
			out.ToolsAndMCP = proto.Int(buckets[1].alloc)
		}
		if buckets[2].present {
			out.Conversation = proto.Int(buckets[2].alloc)
		}
		if buckets[3].present {
			out.ToolOutputs = proto.Int(buckets[3].alloc)
		}
	}

	if contextWindow > 0 {
		out.Free = proto.Int(max(contextWindow-contextTokens, 0))
	}
	if autoCompactThreshold > 0 {
		out.AutoCompactThreshold = proto.Int(autoCompactThreshold)
	}
	if !out.Valid() {
		return nil
	}
	return out
}
