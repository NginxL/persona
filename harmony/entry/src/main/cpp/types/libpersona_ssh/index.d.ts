export interface NativeSshConfig {
  id: string;
  sshHost: string;
  sshPort: number;
  username: string;
  hostKey: string;
  targetHost: string;
  targetPort: number;
  resolvedHubIps: string[];
  privateKey: Uint8Array;
  passphrase: Uint8Array;
}
export interface NativeSshLease { id: string; host: string; port: number; username: string; password: string; }
export interface NativeSshBridge {
  open(config: NativeSshConfig): Promise<NativeSshLease>;
  close(id: string): Promise<void>;
  isOpen(id: string): boolean;
}
declare const bridge: NativeSshBridge;
export default bridge;
