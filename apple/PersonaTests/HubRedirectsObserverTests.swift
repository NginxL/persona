import Foundation
import os
import PersonaWire
import Testing

/// Answers every request with an empty 200, so a session finishes a task and hands over its metrics without a network.
private final class EmptyOKProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: [:])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Keeps the first finished task and its metrics.
private final class MetricsCatcher: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    private let done: OSAllocatedUnfairLock<CheckedContinuation<(URLSessionTask, URLSessionTaskMetrics), Never>?>
    init(_ done: CheckedContinuation<(URLSessionTask, URLSessionTaskMetrics), Never>) {
        self.done = OSAllocatedUnfairLock(initialState: done)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didFinishCollecting metrics: URLSessionTaskMetrics) {
        done.withLock { c in
            c?.resume(returning: (task, metrics))
            c = nil
        }
    }
}

/// The address of a local of a function that is never inlined: how far down the stack the caller is.
@inline(never) private func stackAddress() -> UInt {
    var mark = 0
    return withUnsafeMutablePointer(to: &mark) { UInt(bitPattern: $0) }
}

/// `HubRedirects.finished`, which every finished request to the hub reads while Developing Persona records (#12624).
@Suite struct HubRedirectsObserverTests {
    /// One real finished task and its metrics, from a session that never leaves the process.
    private func finishedTask() async -> (URLSessionTask, URLSessionTaskMetrics) {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [EmptyOKProtocol.self]
        return await withCheckedContinuation { done in
            let session = URLSession(configuration: config, delegate: MetricsCatcher(done), delegateQueue: nil)
            session.dataTask(with: URL(string: "https://observer.invalid/api/v1/sessions")!).resume()
            session.finishTasksAndInvalidate()
        }
    }

    /// Bytes of stack one call through `finished` takes, or nil when another test replaced the closure meanwhile
    /// (`DevMode` sets and clears it).
    private func stackOfOneCall(_ task: URLSessionTask, _ metrics: URLSessionTaskMetrics) -> UInt? {
        let used = OSAllocatedUnfairLock<UInt?>(initialState: nil)
        let top = stackAddress()
        HubRedirects.finished = { @Sendable _, _ in
            let here = stackAddress()
            used.withLock { $0 = top - here }
        }
        HubRedirects.finished?(task, metrics)
        let first = used.withLock { $0 }
        used.withLock { $0 = nil }
        // Every finished request reads the closure once: thousands of reads in a long recording.
        for _ in 0..<2000 { _ = HubRedirects.finished }
        HubRedirects.finished?(task, metrics)
        guard let first, let later = used.withLock({ $0 }) else { return nil }
        return later > first ? later - first : 0
    }

    /// Reads leave the closure as it was. A bare function type as the lock's state came back from each read wrapped
    /// in two more reabstraction thunks, so after a few thousand requests one call overflowed the URLSession delegate
    /// queue's 512 KB stack and the app crashed.
    @Test func readingTheObserverDoesNotDeepenTheNextCall() async throws {
        let (task, metrics) = await finishedTask()
        let previous = HubRedirects.finished
        defer { HubRedirects.finished = previous }
        var grown: UInt?
        for _ in 0..<5 where grown == nil { grown = stackOfOneCall(task, metrics) }
        let growth = try #require(grown, "another test kept replacing HubRedirects.finished")
        // Unchanged it is 0; each read used to add two thunk frames (2000 reads: tens of KB).
        #expect(growth < 1024, "one call through HubRedirects.finished grew by \(growth) bytes after 2000 reads")
    }
}
