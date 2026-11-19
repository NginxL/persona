import AVFoundation
import Speech

// The on-device engines and timestamp-based sentence splitting come from Hourhand's SpeechEngines.swift, without the cloud path.

nonisolated protocol RecordingSpeechEngine: Sendable {
    /// Stored in `Recording.engine`: "analyzer", "dictation", "speech", "cloud".
    var id: String { get }
    var isCloud: Bool { get }
    func transcribe(_ url: URL, locale: Locale) async throws -> [RecordingSentence]
}

nonisolated enum RecordingSpeechError: LocalizedError {
    case unsupportedLanguage
    case noSpeechPermission
    case modelUnavailable
    /// iOS 18: on-device transcription (SpeechAnalyzer) came with iOS 26 (#6730).
    case needsNewerSystem
    case empty

    var errorDescription: String? {
        switch self {
        case .unsupportedLanguage: String(localized: "This device has no on-device transcription model for the selected language")
        case .noSpeechPermission: String(localized: "Speech recognition is off. Allow it for Persona in Settings.")
        case .modelUnavailable: String(localized: "Couldn't download the on-device speech model. Check your network and try again.")
        case .needsNewerSystem: String(localized: "Transcribing on this device needs iOS 26 or later. You can transcribe recordings on your hub instead.")
        case .empty: String(localized: "No speech was recognized in the recording")
        }
    }

    /// About the device rather than the audio (no permission, no model, a language it has no model for): every other
    /// file would fail the same way, so all-day recording's lane stops without counting a try against its chunks.
    var isEntryLevel: Bool {
        switch self {
        case .unsupportedLanguage, .noSpeechPermission, .modelUnavailable, .needsNewerSystem: true
        case .empty: false
        }
    }

    /// About one language only (no model for it, or its model not installed): audio in another language still
    /// transcribes, so all-day recording's lane stops only the days in this one.
    var isPerLanguage: Bool {
        switch self {
        case .unsupportedLanguage, .modelUnavailable: true
        case .noSpeechPermission, .needsNewerSystem, .empty: false
        }
    }
}

/// All-day recording's runs (docs/assistant/day-recap.md): a few contiguous chunk files read as one stretch of audio, so
/// words and sentences that cross a chunk's edge are not cut, and each sentence handed back to the file it starts in.
nonisolated enum RecordingRun {
    /// A sentence starting this little before a file's first sample (the recognizer's rounding) still belongs to it.
    static let startSlack = 0.001

    /// `sentences` on the run's decoded timeline, each given to the file its start falls in, with times on that file's
    /// own clock. `bounds`: where each file starts on the decoded timeline, then where the last one ends (one more value
    /// than files, ascending). `durations`: each file's length as its chunk's header says.
    ///
    /// A time inside a file maps from its decoded samples onto its header length, and a time in a later file onto that
    /// file's length after this one's, so the header lengths add up end to end. A sentence then ends past its file's
    /// `duration` exactly when its audio reaches into the next file, and never past the run's last one: what the day's
    /// erase needs to know (C8, `RecordingDayRuns.bleeding`). Decoding and the header can differ by an encoder frame;
    /// without this mapping, a sentence ending in the next file's first frame could look like it stayed inside its own.
    static func assign(_ sentences: [RecordingSentence], bounds: [Double], durations: [Double]) -> [(file: Int, sentence: RecordingSentence)] {
        let files = bounds.count - 1
        guard files > 0 else { return [] }
        // Header lengths that don't fit (a count that differs, a length that isn't positive) fall back on the decoded ones.
        let lengths = (0..<files).map { file -> Double in
            let header = durations.indices.contains(file) ? durations[file] : 0
            return header.isFinite && header > 0 ? header : max(0, bounds[file + 1] - bounds[file])
        }
        var offsets = [0.0]
        for length in lengths { offsets.append(offsets[offsets.count - 1] + length) }
        // Seconds since the run's start on the header clock, for a time on the decoded one.
        func headerTime(_ t: Double) -> Double {
            let clamped = min(max(t.isFinite ? t : 0, bounds[0]), bounds[files])
            let file = (0..<files).last { bounds[$0] <= clamped } ?? 0
            let decoded = bounds[file + 1] - bounds[file]
            let into = decoded > 0 ? (clamped - bounds[file]) / decoded * lengths[file] : 0
            return offsets[file] + min(max(0, into), lengths[file])
        }
        return sentences.map { sentence in
            let file = (0..<files).last { bounds[$0] <= sentence.start + startSlack } ?? 0
            let start = max(0, headerTime(sentence.start) - offsets[file])
            let end = max(start, headerTime(sentence.end) - offsets[file])
            return (file, RecordingSentence(start: start, end: end, text: sentence.text))
        }
    }

    /// Decodes the files in order into buffers of `format` (each file's own when nil) on one timeline that starts at 0
    /// and advances by the samples decoded, so a sentence's time maps back to its file by sample count, not by the wall
    /// clock. Returns the analyzer's inputs and the files' bounds on that timeline (`assign`). A file that can't be read
    /// throws (the run failed, not the lane).
    @available(iOS 26.0, macOS 26.0, *)
    static func decode(_ urls: [URL], to format: AVAudioFormat?) throws -> (inputs: [AnalyzerInput], bounds: [Double]) {
        var inputs: [AnalyzerInput] = []
        var starts: [Double] = []
        var position: AVAudioFramePosition = 0
        // Without a format from the analyzer, the first file's: one timeline needs one sample rate.
        var common = format
        for url in urls {
            let file = try AVAudioFile(forReading: url)
            let target = common ?? file.processingFormat
            common = target
            starts.append(Double(position) / target.sampleRate)
            guard file.length > 0, let raw = AVAudioPCMBuffer(pcmFormat: file.processingFormat,
                                                              frameCapacity: AVAudioFrameCount(file.length)) else { continue }
            try file.read(into: raw)
            let buffer = try convert(raw, to: target)
            guard buffer.frameLength > 0 else { continue }
            let at = CMTime(value: CMTimeValue(position), timescale: CMTimeScale(target.sampleRate.rounded()))
            inputs.append(AnalyzerInput(buffer: buffer, bufferStartTime: at))
            position += AVAudioFramePosition(buffer.frameLength)
        }
        // Where the last file ends: a sentence can't reach past the run's audio.
        if let rate = common?.sampleRate, rate > 0 { starts.append(Double(position) / rate) }
        return (inputs, starts)
    }

    /// One file's samples in the analyzer's format; the same buffer when it already is.
    private static func convert(_ buffer: AVAudioPCMBuffer, to format: AVAudioFormat) throws -> AVAudioPCMBuffer {
        guard buffer.format != format else { return buffer }
        let ratio = format.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount((Double(buffer.frameLength) * ratio).rounded(.up)) + 1024
        guard let converter = AVAudioConverter(from: buffer.format, to: format),
              let output = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else {
            throw CocoaError(.fileReadCorruptFile)
        }
        var handed = false
        var failure: NSError?
        // The whole file in one go, then the end: a converter that resamples flushes its tail into `output`.
        let status = converter.convert(to: output, error: &failure) { _, state in
            if handed {
                state.pointee = .endOfStream
                return nil
            }
            handed = true
            state.pointee = .haveData
            return buffer
        }
        if status == .error { throw failure ?? CocoaError(.fileReadCorruptFile) }
        return output
    }
}

/// Groups timed words / tokens into sentences: ends at terminal punctuation, on a long pause, or when very long.
nonisolated enum RecordingSentenceBuilder {
    static let terminators: Set<Character> = [".", "?", "!", "。", "？", "！", "…", "؟", "।"] // i18n-ignore

    /// Languages written without spaces between words.
    static func usesSpaces(_ locale: Locale) -> Bool {
        !["zh", "ja", "th", "yue", "lo", "km", "my", "bo"].contains(locale.language.languageCode?.identifier ?? "")
    }

    static func build(_ tokens: [(text: String, start: Double, end: Double)]) -> [RecordingSentence] {
        var out: [RecordingSentence] = []
        var text = "", start = 0.0, end = 0.0
        func flush() {
            let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
            if !t.isEmpty { out.append(RecordingSentence(start: start, end: max(end, start), text: t)) }
            text = ""
        }
        for token in tokens {
            if !text.isEmpty, token.start - end > 1.5 { flush() }
            if text.isEmpty { start = token.start }
            text += token.text
            end = token.end
            let trimmed = token.text.trimmingCharacters(in: .whitespaces)
            if let last = trimmed.last, terminators.contains(last) { flush() } else if text.count > 320 { flush() }
        }
        flush()
        return out
    }
}

// MARK: - iOS 26: SpeechAnalyzer

@available(iOS 26.0, macOS 26.0, *)
nonisolated struct RecordingAnalyzerEngine: RecordingSpeechEngine {
    enum Kind { case speech, dictation }
    let kind: Kind
    var id: String { kind == .speech ? "analyzer" : "dictation" }
    var isCloud: Bool { false }

    /// The best on-device module for a locale, or nil if neither supports it.
    static func best(for locale: Locale) async -> (RecordingAnalyzerEngine, Locale)? {
        if SpeechTranscriber.isAvailable, let l = await SpeechTranscriber.supportedLocale(equivalentTo: locale) {
            return (RecordingAnalyzerEngine(kind: .speech), l)
        }
        if let l = await DictationTranscriber.supportedLocale(equivalentTo: locale) {
            return (RecordingAnalyzerEngine(kind: .dictation), l)
        }
        return nil
    }

    private func module(_ locale: Locale) -> any SpeechModule {
        switch kind {
        case .speech:
            SpeechTranscriber(locale: locale, transcriptionOptions: [], reportingOptions: [], attributeOptions: [.audioTimeRange])
        case .dictation:
            DictationTranscriber(locale: locale, contentHints: [], transcriptionOptions: [.punctuation], reportingOptions: [], attributeOptions: [.audioTimeRange])
        }
    }

    /// Downloads the on-device model if needed (Apple's asset, shared with other apps).
    /// Download / install failures surface as `.modelUnavailable` so the user sees what to do, not the system's raw error.
    static func ensureAssets(_ modules: [any SpeechModule]) async throws {
        guard let request = try await AssetInventory.assetInstallationRequest(supporting: modules) else { return }
        do {
            try await request.downloadAndInstall()
        } catch {
            // A cancelled download may throw a framework error; keep it a cancellation so no error is shown.
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            throw RecordingSpeechError.modelUnavailable
        }
    }

    func transcribe(_ url: URL, locale: Locale) async throws -> [RecordingSentence] {
        let module = module(locale)
        try await Self.ensureAssets([module])
        let file = try AVAudioFile(forReading: url)
        let analyzer = SpeechAnalyzer(modules: [module], options: .init(priority: .userInitiated, modelRetention: .processLifetime))
        let collector = Task { () throws -> [(String, Double, Double)] in
            var tokens: [(String, Double, Double)] = []
            switch module {
            case let t as SpeechTranscriber:
                for try await result in t.results { tokens += Self.tokens(result.text, fallback: result.range) }
            case let t as DictationTranscriber:
                for try await result in t.results { tokens += Self.tokens(result.text, fallback: result.range) }
            default:
                break
            }
            return tokens
        }
        let tokens = try await withTaskCancellationHandler {
            do {
                if let last = try await analyzer.analyzeSequence(from: file) {
                    try await analyzer.finalizeAndFinish(through: last)
                } else {
                    await analyzer.cancelAndFinishNow()
                }
                return try await collector.value
            } catch {
                await analyzer.cancelAndFinishNow()
                collector.cancel()
                throw error
            }
        } onCancel: {
            collector.cancel()
            Task { await analyzer.cancelAndFinishNow() }
        }
        try Task.checkCancellation()
        return RecordingSentenceBuilder.build(tokens.map { (text: $0.0, start: $0.1, end: $0.2) })
    }

    /// All-day recording's run (C8): the files' audio fed in order into ONE analyzer session, so a word or sentence that
    /// crosses a chunk's edge comes out whole, and the analyzer is set up once per run instead of once per 10 s chunk.
    /// Each sentence goes to the file its start is in (`file` indexes `urls`), its times on that file's clock as its
    /// header's `durations` measure it (`RecordingRun.assign`: past the duration only when it ran into the next file).
    /// `downloadsAssets` false (in the background) never downloads the model: a model that isn't on the device
    /// throws `.modelUnavailable`, which stops the lane for the days in that language rather than failing the run.
    /// `@concurrent`: the files are decoded off the main actor whoever calls.
    @concurrent func transcribeRun(_ urls: [URL], durations: [Double], locale: Locale, priority: TaskPriority,
                                   downloadsAssets: Bool) async throws -> [(file: Int, sentence: RecordingSentence)] {
        guard !urls.isEmpty else { return [] }
        let module = module(locale)
        if downloadsAssets {
            try await Self.ensureAssets([module])
        } else if await AssetInventory.status(forModules: [module]) != .installed {
            throw RecordingSpeechError.modelUnavailable
        }
        let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [module])
        let decoded = try RecordingRun.decode(urls, to: format) // perf-ok: @concurrent, off the main actor
        guard !decoded.inputs.isEmpty else { return [] }
        let (input, feed) = AsyncStream.makeStream(of: AnalyzerInput.self)
        for buffer in decoded.inputs { feed.yield(buffer) }
        feed.finish()
        // `.lingering`: the model stays loaded between the runs of a backlog, and is let go once the lane is idle.
        let analyzer = SpeechAnalyzer(modules: [module], options: .init(priority: priority, modelRetention: .lingering))
        let collector = Task { () throws -> [(String, Double, Double)] in
            var tokens: [(String, Double, Double)] = []
            switch module {
            case let t as SpeechTranscriber:
                for try await result in t.results { tokens += Self.tokens(result.text, fallback: result.range) }
            case let t as DictationTranscriber:
                for try await result in t.results { tokens += Self.tokens(result.text, fallback: result.range) }
            default:
                break
            }
            return tokens
        }
        let tokens = try await withTaskCancellationHandler {
            do {
                if let last = try await analyzer.analyzeSequence(input) {
                    try await analyzer.finalizeAndFinish(through: last)
                } else {
                    await analyzer.cancelAndFinishNow()
                }
                return try await collector.value
            } catch {
                await analyzer.cancelAndFinishNow()
                collector.cancel()
                throw error
            }
        } onCancel: {
            collector.cancel()
            Task { await analyzer.cancelAndFinishNow() }
        }
        try Task.checkCancellation()
        let sentences = RecordingSentenceBuilder.build(tokens.map { (text: $0.0, start: $0.1, end: $0.2) })
        return RecordingRun.assign(sentences, bounds: decoded.bounds, durations: durations)
    }

    /// Runs of a result with their audio time ranges.
    private static func tokens(_ text: AttributedString, fallback: CMTimeRange) -> [(String, Double, Double)] {
        var out: [(String, Double, Double)] = []
        for run in text.runs {
            let piece = String(text[run.range].characters)
            if let range = run[AttributeScopes.SpeechAttributes.TimeRangeAttribute.self] {
                out.append((piece, range.start.seconds, range.end.seconds))
            } else if let last = out.last {
                // Punctuation and spaces carry no time: attach to the previous token.
                out[out.count - 1] = (last.0 + piece, last.1, last.2)
            } else {
                out.append((piece, fallback.start.seconds, fallback.start.seconds))
            }
        }
        return out
    }
}
