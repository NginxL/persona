import type { ExtensionUIStreamSnapshot, ExtensionViewRecord } from './ExtensionViewTree';

/** The read and interaction surface shared by native extension view hosts. */
export interface ExtensionPresentation {
  readonly presentationKey?: string;
  view: ExtensionViewRecord;
  busy: boolean;
  error: string;
  revision: number;
  viewVersion: number;
  lineResultRevision: number;
  canInteract(): boolean;
  fieldValue(key: string, fallback?: string): string;
  setFieldValue(key: string, value: string, secret?: boolean): void;
  toggleValue(key: string, choice: string): string;
  getSnapshot(): ExtensionUIStreamSnapshot | undefined;
  lineResultFor(key: string): ExtensionViewRecord;
  performAction(key: string, actionId: string, event?: string, ownValues?: ExtensionViewRecord, title?: string): Promise<void>;
  imageBytes(path: string): Promise<ArrayBuffer>;
}
