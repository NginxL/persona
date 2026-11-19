import { socket } from '@kit.NetworkKit';

// HDC reverse forwarding accepts IPv4 loopback. Give the transport an IPv6-only ingress.
export async function startIPv6Relay(listenPort: number, backendPort: number): Promise<socket.TCPSocketServer> {
  const listener = socket.constructTCPSocketServerInstance();
  listener.on('connect', (incoming: socket.TCPSocketConnection): void => {
    const outgoing = socket.constructTCPSocketInstance();
    let ended = false;
    const close = (): void => {
      if (ended) return;
      ended = true;
      incoming.close().catch((): void => {});
      outgoing.close().catch((): void => {});
    };
    incoming.on('error', close); outgoing.on('error', close);
    incoming.on('close', close); outgoing.on('close', close);
    let downstream = Promise.resolve();
    outgoing.on('message', (data: socket.SocketMessageInfo): void => {
      downstream = downstream.then(() => incoming.send({ data: data.message })).catch(close);
    });
    let upstream = outgoing.connect({ address: { address: '127.0.0.1', port: backendPort, family: 1 }, timeout: 3000 }).catch(close);
    incoming.on('message', (data: socket.SocketMessageInfo): void => {
      upstream = upstream.then(() => outgoing.send({ data: data.message })).catch(close);
    });
  });
  await listener.listen({ address: '::1', port: listenPort, family: 2 });
  return listener;
}
