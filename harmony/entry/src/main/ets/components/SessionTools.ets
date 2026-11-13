import { common } from '@kit.AbilityKit';
import { fileIo, fileUri, picker } from '@kit.CoreFileKit';
import { filePreview } from '@kit.PreviewKit';
import { util } from '@kit.ArkTS';
import { ComposeListItemV2, ContentItemV2, LengthMetrics } from '@kit.ArkUI';
import { image } from '@kit.ImageKit';
import { ClientModel } from '../models/ClientModel';
import { SessionWorkspace, WorkspaceEntry, WorkspaceGitFile, WorkspacePreviewServer, WorkspaceBranch, WorkspaceTrashItem } from '../models/SessionWorkspace';
import { FileTransferMachine, FileTransferRecord, FileTransferSetting } from '../models/FileTransfer';
import { Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { readerLink } from '../platform/ReaderLink';

const TRANSFER_LIMIT: number = 32 * 1024 * 1024;

class PickedFile {
  name: string = '';
  data: ArrayBuffer = new ArrayBuffer(0);
}

class FilePreviewCopy {
  name: string = '';
  path: string = '';
}

@ComponentV2
export struct SessionTools {
  @Require @Param model: ClientModel;
  @Param initialFile: string = '';
  @Local workspace: SessionWorkspace = new SessionWorkspace();
  @Local fileRowBindings: BoundRow<WorkspaceEntry>[] = [];
  @Local gitRowBindings: BoundRow<WorkspaceGitFile>[] = [];
  @Local stashRowBindings: BoundRow<Record<string, Object>>[] = [];
  @Local previewRowBindings: BoundRow<WorkspacePreviewServer>[] = [];
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  private fileBindingScope: string = '';
  @Local pathDraft: string = '';
  @Local terminalInput: string = '';
  @Local transferError: string = '';
  @Local fileOpen: boolean = false;
  @Local transferOpen: boolean = false;
  @Local fileEditSheetOpen: boolean = false;
  @Local fileEditMode: string = '';
  @Local fileActionEntry: WorkspaceEntry | undefined = undefined;
  @Local renameDraft: string = '';
  @Local moveTarget: string = '';
  @Local moveConflictChoice: number = 0;
  @Local fileEditBusy: boolean = false;
  @Local diffOpen: boolean = false;
  @Local pickerBusy: boolean = false;
  @Local previewBusy: boolean = false;
  @Local previewPixels: image.PixelMap | undefined = undefined;
  @Local previewRatio: number = 1;
  @Local previewServerIndex: number = 0;
  @Local previewStartConfirmName: string = '';
  @Local screenInput: string = '';
  @Local screenControlConfirmOpen: boolean = false;
  @Local terminalControlConfirmOpen: boolean = false;
  @Local gitSheetOpen: boolean = false;
  @Local gitSheetMode: string = '';
  @Local gitDraftMessage: string = '';
  @Local gitStashMessage: string = '';
  @Local gitStashUntracked: boolean = false;
  @Local gitSelectedBranch: number = 0;
  @Local gitTargetBranch: string = '';
  @Local gitNewBranchName: string = '';
  @Local previewPathDraft: string = '/';
  @Local previewInput: string = '';
  private previewCopies: FilePreviewCopy[] = [];
  @Event onWorkspaceReady: (workspace: SessionWorkspace | undefined) => void = (_workspace: SessionWorkspace | undefined) => {};
  @Event onOpenLink: (address: string) => void = (_address: string) => {};

  aboutToAppear(): void {
    this.bindWorkspace();
    if (this.initialFile) { this.fileOpen = true; this.workspace.openFile(this.initialFile, this.initialFile.split('/').pop() || this.initialFile); }
  }

  private bindWorkspace(): void {
    this.workspace.bind(this.model);
    this.syncRowBindings();
    this.pathDraft = this.workspace.currentPath;
    this.workspace.show();
    this.fileOpen = false; this.fileEditSheetOpen = false; this.diffOpen = false;
    this.fileActionEntry = undefined; this.fileEditMode = '';
    this.transferOpen = false;
    this.fileEditBusy = false; this.pickerBusy = false; this.previewBusy = false; this.transferError = '';
    this.previewPathDraft = '/'; this.previewInput = '';
    this.previewPixels?.release(); this.previewPixels = undefined;
    this.onWorkspaceReady(this.workspace);
  }

  aboutToDisappear(): void {
    this.workspace.hide();
    this.previewPixels?.release(); this.previewPixels = undefined;
    this.onWorkspaceReady(undefined);
  }

  @Monitor('model.selected') selectedChanged(): void {
    const cwd = this.model.selected.path || Wire.text(this.model.selected.data(), 'cwd');
    if (this.workspace.isCurrent() && this.workspace.cwd === cwd) return;
    this.bindWorkspace();
  }

  @Monitor('model.profile') profileChanged(): void {
    this.bindWorkspace();
  }

  @Monitor('model.connected') connectionChanged(): void {
    if (this.model.connected) this.workspace.onConnected();
    else this.workspace.onDisconnected();
  }

  @Monitor('fileOpen') fileSheetChanged(): void {
    if (!this.fileOpen) this.workspace.closeFile();
  }

  @Monitor('diffOpen') diffSheetChanged(): void {
    if (!this.diffOpen) this.workspace.closeGitDiff();
  }

  @Monitor('workspace.entries', 'workspace.gitFiles', 'workspace.gitStashes', 'workspace.previewServers',
    'workspace.currentPath', 'workspace.cwd', 'model.profile', 'model.selected', 'model.machines')
  rowsChanged(): void { this.syncRowBindings(); this.workspace.fileTransfer.refreshTargets(); }

  private syncRowBindings(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager,
      this.model.selected.key(), this.workspace.cwd]);
    const fileScope = JSON.stringify([scope, this.workspace.currentPath]);
    this.fileRowBindings = reconcileBoundRows(this.fileRowBindings, this.workspace.entries,
      (row: WorkspaceEntry) => row.path, this.rowBindingOwner, this.model, this.fileBindingScope, fileScope);
    this.gitRowBindings = reconcileBoundRows(this.gitRowBindings, this.workspace.gitFiles,
      (row: WorkspaceGitFile) => row.path, this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.stashRowBindings = reconcileBoundRows(this.stashRowBindings, this.workspace.gitStashes,
      (row: Record<string, Object>) => Wire.text(row, 'oid'), this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.previewRowBindings = reconcileBoundRows(this.previewRowBindings, this.workspace.previewServers,
      (row: WorkspacePreviewServer) => row.name, this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.rowBindingOwner = this.model; this.rowBindingScope = scope; this.fileBindingScope = fileScope;
  }

  @Monitor('workspace.previewFrame') previewFrameChanged(): void { this.decodePreviewFrame(); }

  private uiContext(): common.UIAbilityContext {
    return this.getUIContext().getHostContext() as common.UIAbilityContext;
  }

  private errorText(error: Error): string {
    if (error.message === 'transfer_limit' || error.message === 'response_too_large') return tr('session_tools_transfer_limit');
    if (error.message === 'transfer_unavailable' || error.message === 'transfer_active' || error.message === 'target_unavailable') return tr('file_transfer_unavailable');
    if (error.message === 'journal_unavailable') return tr('file_transfer_journal_unavailable');
    if (error.message === 'transfer_unknown_requires_acknowledgement') return tr('file_transfer_check_unknown_first');
    if (error.message === 'duplicate_confirmation_required') return tr('file_transfer_confirm_duplicate');
    if (error.message === 'scope_changed' || error.message === 'request_updated') return tr('request_updated');
    if (error.message === 'file_read_failed') return tr('session_tools_file_read_failed');
    if (error.message === 'preview_unavailable') return tr('session_tools_preview_unavailable');
    if (error.message === 'write_unavailable' || error.message === 'pending_action') return tr('session_tools_write_unavailable');
    return tr('failed') + ': ' + error.message;
  }

  private openEntry(entry: WorkspaceEntry): void {
    if (!entry.directory) this.fileOpen = true;
    this.workspace.openEntry(entry).then(() => this.pathDraft = this.workspace.currentPath)
      .catch((error: Error) => this.transferError = this.errorText(error));
  }

  private openFileTransfer(entry: WorkspaceEntry): void {
    this.transferError = '';
    this.transferOpen = true;
    this.workspace.openFileTransfer(entry).catch((error: Error) => this.transferError = this.errorText(error));
  }

  private selectTransferTarget(machineId: string): void {
    if (this.workspace.fileTransfer.targetSelectionFrozen()) return;
    this.workspace.fileTransfer.selectTarget(machineId).catch((error: Error) => this.transferError = this.errorText(error));
  }

  private currentTransfer(): FileTransferRecord | undefined { return this.workspace.fileTransfer.latestForSource(); }

  private confirmFileTransfer(): void {
    const transfer = this.workspace.fileTransfer;
    const machineId = transfer.targetId;
    if (!transfer.canStart(machineId)) return;
    const duplicate = transfer.requiresDuplicateConfirmation(machineId);
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    this.getUIContext().showAlertDialog({ title: tr('file_transfer_confirm_title'),
      message: this.transferConfirmation(duplicate),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('file_transfer_confirm_send'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget() ||
          machineId !== transfer.targetId) return;
        transfer.start(machineId, duplicate).then((record: FileTransferRecord) => {
          if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
          this.transferError = '';
          if (record.state === 'failed' && record.code) this.transferError = tr('file_transfer_error') + ': ' + record.code;
        }).catch((error: Error) => {
          if (this.workspace.isCurrent() && revision === this.workspace.currentRevision() && target === this.workspace.currentTarget())
            this.transferError = this.errorText(error);
        });
      } }
    });
  }

  private transferConfirmation(duplicate: boolean): string {
    const transfer = this.workspace.fileTransfer;
    const lines = [tr('file_transfer_source') + ': ' + transfer.sourceMachineName(), transfer.sourceFilePath(),
      tr('file_transfer_target') + ': ' + transfer.targetMachineName(),
      tr('file_transfer_inbox') + ': ' + transfer.settingForTarget().inbox];
    if (duplicate) lines.push(tr('file_transfer_confirm_duplicate'));
    return lines.filter((line: string) => !!line).join('\n');
  }

  private currentTransferProgress(): number {
    const record = this.currentTransfer();
    return record ? this.workspace.fileTransfer.progress(record) : 0;
  }

  private currentTransferStatus(): string {
    const record = this.currentTransfer();
    if (!record) return '';
    let status = tr('file_transfer_status_' + record.state);
    if (record.total > 0) status += ' · ' + String(record.bytes) + ' / ' + String(record.total);
    return status;
  }

  private currentTransferCancelKey(): string { return this.currentTransfer()?.key || ''; }

  private checkTransfer(key: string): void {
    this.workspace.fileTransfer.refresh(key).catch((error: Error) => this.transferError = this.errorText(error));
  }

  private acknowledgeTransfer(key: string): void {
    this.workspace.fileTransfer.acknowledgeUnknown(key).then((saved: boolean) => {
      if (!saved) this.transferError = tr('file_transfer_journal_unavailable');
    });
  }

  private cancelTransfer(key: string): void {
    this.workspace.fileTransfer.cancel(key).then(() => {});
  }

  private openPath(): void {
    const next = this.pathDraft.trim();
    if (!next) return;
    this.workspace.openPath(next).catch((error: Error) => this.transferError = this.errorText(error));
  }

  private selectedGitFile(): WorkspaceGitFile | undefined {
    return this.workspace.gitFiles.find((file: WorkspaceGitFile) => file.path === this.workspace.gitDiffPath);
  }

  private gitDiffHasBoth(): boolean {
    const file = this.selectedGitFile();
    return !!file && file.staged && file.unstaged;
  }

  private openGitFile(file: WorkspaceGitFile): void {
    this.diffOpen = true;
    this.workspace.openGitDiff(file);
  }

  private openGitSheet(mode: string): void {
    if (!this.workspace.canWriteGit()) return;
    this.gitSheetMode = mode;
    if (mode === 'branch') {
      this.gitSelectedBranch = Math.max(0, this.workspace.gitBranches.findIndex((branch: WorkspaceBranch) => !branch.current));
      this.gitNewBranchName = '';
      this.workspace.loadGitBranches();
    } else if (mode === 'commit') this.gitDraftMessage = '';
    else if (mode === 'stash_push') { this.gitStashMessage = ''; this.gitStashUntracked = false; }
    this.gitSheetOpen = true;
  }

  private branchLabel(branch: WorkspaceBranch): string {
    return branch.name + (branch.current ? ' · ' + tr('session_tools_current_branch') : '') + (branch.remote ? ' · ' + tr('session_tools_remote_branch') : '');
  }

  private confirmGitWrite(kind: string, value: string = '', extra: string = ''): void {
    if (!this.workspace.canWriteGit()) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    let payloadValue = value, payloadExtra = extra;
    if (kind === 'commit') payloadValue = this.gitDraftMessage.trim();
    else if (kind === 'switch') payloadValue = this.workspace.gitBranches[this.gitSelectedBranch]?.name || '';
    else if (kind === 'create') {
      payloadValue = this.gitNewBranchName.trim();
      payloadExtra = this.workspace.gitBranches[this.gitSelectedBranch]?.name || Wire.text(this.workspace.gitStatus, 'branch');
    } else if (kind === 'stash_push') {
      payloadValue = this.gitStashMessage.trim(); payloadExtra = this.gitStashUntracked ? '1' : '';
    }
    if (kind === 'commit' && !payloadValue || (kind === 'switch' || kind === 'create') && !payloadValue) return;
    const branch = Wire.text(this.workspace.gitStatus, 'branch', tr('branch'));
    const machine = this.model.machineName(this.workspace.currentMachineId());
    let detail = tr('session_tools_git_' + kind);
    if (payloadValue) detail += '\n' + payloadValue;
    if (kind === 'push') detail += '\n' + Wire.text(this.workspace.gitStatus, 'upstream', tr('session_tools_no_upstream'));
    if (kind === 'publish') detail += '\n' + branch;
    if (kind === 'create' && payloadExtra) detail += '\n' + tr('session_tools_branch_from') + ': ' + payloadExtra;
    this.getUIContext().showAlertDialog({ title: tr('session_tools_git_confirm_title'),
      message: machine + '\n' + this.workspace.cwd + '\n' + branch + '\n' + detail,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('continue'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        this.runGitWrite(kind, payloadValue, payloadExtra, revision, target);
      } }
    });
  }

  private runGitWrite(kind: string, value: string, extra: string, revision: number, target: string): void {
    let action: Promise<boolean> = Promise.resolve(false);
    if (kind === 'stage' || kind === 'unstage') {
      const file = this.workspace.gitFiles.find((candidate: WorkspaceGitFile) => candidate.path === value);
      if (file) action = this.workspace.stageGitFile(file, kind === 'unstage');
    } else if (kind === 'commit') action = this.workspace.commitGit(value);
    else if (kind === 'push') action = this.workspace.pushGit(false);
    else if (kind === 'publish') action = this.workspace.pushGit(true);
    else if (kind === 'switch') action = this.workspace.switchGit(value);
    else if (kind === 'create') action = this.workspace.switchGit(value, true, extra);
    else if (kind === 'stash_push') action = this.workspace.stashGit('push', '', value, extra === '1');
    else if (kind === 'stash_apply' || kind === 'stash_pop' || kind === 'stash_drop')
      action = this.workspace.stashGit(kind.slice('stash_'.length), value);
    action.then((ok: boolean) => {
      if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
      if (!ok) this.transferError = this.errorText(new Error(this.workspace.gitWriteError || 'failed'));
      else { this.transferError = ''; this.gitSheetOpen = false; }
    });
  }

  private openFileActions(mode: string, entry: WorkspaceEntry): void {
    this.fileActionEntry = entry; this.fileEditMode = mode;
    if (mode === 'rename') this.renameDraft = entry.name;
    else if (mode === 'move') this.moveTarget = this.workspace.currentPath || this.workspace.cwd;
    this.fileEditSheetOpen = true;
  }

  private confirmFileEdit(): void {
    const entry = this.fileActionEntry;
    if (!entry || !this.workspace.canEditFiles() || this.fileEditBusy) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    const mode = this.fileEditMode;
    const destination = mode === 'rename' ? this.renameDraft.trim() : this.moveTarget.trim();
    const details = mode === 'rename' ? entry.path + ' → ' + destination : entry.path + '\n→ ' + destination;
    const replacing = mode === 'move_conflict' && this.moveConflictChoice === 1;
    this.getUIContext().showAlertDialog({ title: tr(replacing ? 'session_tools_replace_confirm' : 'session_tools_file_edit_confirm'),
      message: this.model.machineName(this.workspace.currentMachineId()) + '\n' + this.workspace.cwd + '\n' + details,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('continue'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget() ||
          this.fileActionEntry?.path !== entry.path) return;
        this.saveFileEdit();
      } }
    });
  }

  private async saveFileEdit(): Promise<void> {
    const entry = this.fileActionEntry;
    if (!entry || this.fileEditBusy) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    this.fileEditBusy = true;
    try {
      if (this.fileEditMode === 'rename') {
        const renamed = await this.workspace.renameFile(entry.path, this.renameDraft);
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        if (!renamed) this.transferError = this.errorText(new Error(this.workspace.fileWriteError || 'failed'));
        else { this.fileEditSheetOpen = false; this.workspace.fileWriteState = 'idle'; }
      } else if (this.fileEditMode === 'move') {
        const moved = await this.workspace.moveFiles([entry.path], this.moveTarget);
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        if (moved) this.fileEditSheetOpen = false;
        else if (this.workspace.fileWriteState === 'conflict') this.fileEditMode = 'move_conflict';
        else this.transferError = this.errorText(new Error(this.workspace.fileWriteError || 'failed'));
      } else if (this.fileEditMode === 'move_conflict') {
        const policy = this.moveConflictChoice === 0 ? 'keep' : 'replace';
        const moved = await this.workspace.moveFiles([entry.path], this.moveTarget, policy);
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        if (moved) this.fileEditSheetOpen = false;
        else this.transferError = this.errorText(new Error(this.workspace.fileWriteError || 'failed'));
      }
    } catch (error) {
      if (this.workspace.isCurrent() && revision === this.workspace.currentRevision() && target === this.workspace.currentTarget())
        this.transferError = this.errorText(error as Error);
    } finally {
      if (this.workspace.isCurrent() && revision === this.workspace.currentRevision() && target === this.workspace.currentTarget()) this.fileEditBusy = false;
    }
  }

  private confirmTrash(entry: WorkspaceEntry): void {
    if (!this.workspace.canEditFiles()) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    const machine = this.model.machineName(this.workspace.currentMachineId());
    this.getUIContext().showAlertDialog({ title: tr('session_tools_trash_title'),
      message: machine + '\n' + this.workspace.cwd + '\n' + entry.path,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('session_tools_move_to_trash'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        this.workspace.trashFiles([entry.path]).then((ok: boolean) => {
          if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
          if (!ok) this.transferError = this.errorText(new Error(this.workspace.fileWriteError || 'failed'));
        });
      } }
    });
  }

  private restoreTrash(): void {
    if (!this.workspace.canEditFiles() || !this.workspace.trashUndo.length) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    const trashed = this.workspace.trashUndo.map((item: WorkspaceTrashItem) => item.path);
    this.getUIContext().showAlertDialog({ title: tr('session_tools_restore_confirm'),
      message: this.model.machineName(this.workspace.currentMachineId()) + '\n' + this.workspace.cwd + '\n' + trashed.join('\n'),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('session_tools_restore'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget() ||
          JSON.stringify(trashed) !== JSON.stringify(this.workspace.trashUndo.map((item: WorkspaceTrashItem) => item.path))) return;
        this.runRestoreTrash(revision, target);
      } }
    });
  }

  private runRestoreTrash(revision: number, target: string): void {
    this.workspace.restoreTrash().then((ok: boolean) => {
      if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
      if (!ok) this.transferError = this.errorText(new Error(this.workspace.fileWriteError || 'failed'));
    });
  }

  private async previewSelectedFile(): Promise<void> {
    if (!this.fileOpen || !this.workspace.selectedFile || this.previewBusy) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget(), path = this.workspace.selectedFile;
    this.previewBusy = true; this.transferError = '';
    try {
      if (!canIUse('SystemCapability.FileManagement.FilePreview.Core')) throw new Error('preview_unavailable');
      const file = await this.workspace.downloadFile();
      if (!this.fileOpen || !this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget() || path !== this.workspace.selectedFile)
        throw new Error('request_updated');
      const context = this.uiContext(), directory = context.cacheDir + '/session-workspace-previews';
      try { fileIo.statSync(directory); } catch (_) { fileIo.mkdirSync(directory, true); }
      this.prunePreviewCopies(directory);
      const extension = this.previewExtension(file.name, this.workspace.selectedFileMime);
      const copy = new FilePreviewCopy(); copy.name = file.name;
      copy.path = directory + '/' + util.generateRandomUUID() + (extension ? '.' + extension : '');
      const output = fileIo.openSync(copy.path, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
      try { if (fileIo.writeSync(output.fd, file.data) !== file.data.byteLength) throw new Error('preview_unavailable'); }
      finally { fileIo.closeSync(output.fd); }
      this.previewCopies.push(copy);
      const uri = fileUri.getUriFromPath(copy.path);
      if (!await filePreview.canPreview(context, uri)) throw new Error('preview_unavailable');
      if (!this.fileOpen || !this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) throw new Error('request_updated');
      await filePreview.openPreview(context, { title: file.name, uri: uri, mimeType: this.workspace.selectedFileMime });
      if (!this.fileOpen || !this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) {
        await filePreview.closePreview(context); throw new Error('request_updated');
      }
    } catch (error) { this.transferError = this.errorText(error as Error); }
    finally { this.previewBusy = false; }
  }

  private previewExtension(name: string, mime: string): string {
    const match = name.match(/\.([A-Za-z0-9]{1,12})$/);
    if (match) return match[1].toLowerCase();
    if (mime === 'application/pdf') return 'pdf';
    if (mime === 'image/jpeg') return 'jpg';
    if (mime === 'image/png') return 'png';
    if (mime === 'image/webp') return 'webp';
    if (mime === 'image/gif') return 'gif';
    if (mime === 'image/heic' || mime === 'image/heif') return 'heic';
    if (mime.startsWith('audio/')) return 'audio';
    if (mime.startsWith('video/')) return 'video';
    return '';
  }

  private prunePreviewCopies(directory: string): void {
    let names: string[] = [];
    try { names = fileIo.listFileSync(directory); } catch (_) { return; }
    const keep = names.filter((name: string) => /^[A-Za-z0-9_-]{16,80}(?:\.[A-Za-z0-9]{1,12})?$/.test(name));
    while (keep.length >= 8) { const old = keep.shift(); if (old) { try { fileIo.unlinkSync(directory + '/' + old); } catch (_) {} } }
  }

  private async decodePreviewFrame(): Promise<void> {
    const frame = this.workspace.previewFrame;
    if (!frame) { this.previewPixels?.release(); this.previewPixels = undefined; return; }
    let source: image.ImageSource | undefined;
    let pixels: image.PixelMap | undefined;
    try {
      source = image.createImageSource(frame.data);
      const info = await source.getImageInfo();
      if (info.size.width <= 0 || info.size.height <= 0 || info.size.width * info.size.height > 40000000) throw new Error('response_too_large');
      const scale = Math.min(1, 1600 / Math.max(info.size.width, info.size.height));
      pixels = await source.createPixelMap({ desiredSize: { width: Math.max(1, Math.round(info.size.width * scale)), height: Math.max(1, Math.round(info.size.height * scale)) } });
      if (this.workspace.previewFrame !== frame || !this.workspace.isCurrent()) { await pixels.release(); return; }
      const old = this.previewPixels;
      this.previewPixels = pixels; pixels = undefined; this.previewRatio = info.size.width / info.size.height;
      old?.release(); this.workspace.acknowledgePreviewFrame(frame.seq);
    } catch (_) {
      if (this.workspace.previewFrame === frame) this.workspace.previewError = 'preview_unavailable';
    } finally { await pixels?.release(); await source?.release(); }
  }

  private openPreviewExternal(): void {
    const address = readerLink(this.workspace.previewExternalUrl);
    if (address) this.onOpenLink(address);
  }

  private previewNavigationPath(): string {
    const path = this.previewPathDraft.trim();
    if (!path.startsWith('/') || path.startsWith('//') || /[\\\r\n\u0000-\u001f]/.test(path) || path.includes('://')) return '';
    return path;
  }

  private sendPreviewNavigation(): void {
    const path = this.previewNavigationPath();
    if (!path) { this.transferError = tr('session_tools_preview_path_invalid'); return; }
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    if (this.workspace.sendPreviewAction(Wire.payload(['t', 'url'], ['nav', path]), revision, target)) {
      this.workspace.previewPath = path; this.transferError = '';
    }
  }

  private previewCommandWord(word: string): string {
    if (word.length > 0 && /^[A-Za-z0-9_./-]+$/.test(word)) return word;
    return "'" + word.replace(/'/g, "'\\''") + "'";
  }

  private previewCommand(server: WorkspacePreviewServer): string {
    return server.command.map((word: string) => this.previewCommandWord(word)).join(' ');
  }

  private confirmStartPreview(server: WorkspacePreviewServer): void {
    if (!this.workspace.canStartPreview()) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    const envKeys = Object.keys(server.env).sort();
    const environment = envKeys.length ? '\nenv ' + envKeys.map((key: string) => this.previewCommandWord(key + '=•••')).join(' ') : '';
    this.getUIContext().showAlertDialog({ title: tr('session_tools_preview_start_confirm'),
      message: this.model.machineName(this.workspace.currentMachineId()) + '\n' + (server.cwd || this.workspace.cwd) + '\n' +
        this.previewCommand(server) + environment,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('start'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        this.workspace.startPreview(server.name).then((ok: boolean) => {
          if (!ok && revision === this.workspace.currentRevision() && target === this.workspace.currentTarget())
            this.transferError = this.errorText(new Error(this.workspace.previewError || 'failed'));
        });
      } }
    });
  }

  private openReadyPreview(server: WorkspacePreviewServer): void {
    if (!this.workspace.canUsePreview() || server.port < 1) return;
    const path = this.previewPathFromUrl(server.url);
    this.previewPathDraft = path;
    this.workspace.openPreview(server.port, path, server.name).then((ok: boolean) => {
      if (ok) this.transferError = '';
      else this.transferError = this.errorText(new Error(this.workspace.previewError || 'preview_unavailable'));
    });
  }

  private previewPathFromUrl(value: string): string {
    if (!value) return '/';
    const marker = value.indexOf('://');
    const slash = marker >= 0 ? value.indexOf('/', marker + 3) : value.indexOf('/');
    const path = slash >= 0 ? value.slice(slash) : '/';
    return path.startsWith('/') && !path.startsWith('//') && !/[\\\r\n]/.test(path) ? path : '/';
  }

  private previewAction(name: string): void {
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    this.workspace.sendPreviewAction(Wire.payload(['t'], [name]), revision, target);
  }

  private sendPreviewText(): void {
    const text = this.previewInput, frame = this.workspace.previewFrame;
    if (!text || !frame) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    if (this.workspace.sendPreviewFrameAction(Wire.payload(['t', 'text'], ['text', text]), frame.seq, revision, target)) this.previewInput = '';
  }

  private confirmTerminalControl(takeover: boolean = false): void {
    if (!this.workspace.canControlTerminal()) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    this.getUIContext().showAlertDialog({ title: tr('session_tools_terminal_control_confirm'),
      message: this.model.machineName(this.workspace.currentMachineId()) + '\n' + this.workspace.cwd,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('continue'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        if (takeover) this.workspace.takeOverTerminal(); else this.workspace.setTerminalControl(true);
      } }
    });
  }

  private confirmScreenControl(): void {
    if (!this.workspace.canControlScreen()) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget();
    this.getUIContext().showAlertDialog({ title: tr('session_tools_terminal_screen_control_confirm'),
      message: this.model.machineName(this.workspace.currentMachineId()) + '\n' + this.workspace.cwd,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('continue'), action: () => {
        if (!this.workspace.isCurrent() || revision !== this.workspace.currentRevision() || target !== this.workspace.currentTarget()) return;
        if (!this.workspace.enableScreenControl()) this.transferError = tr('session_tools_view_only');
      } }
    });
  }

  private sendTerminalScreenText(): void {
    if (!this.screenInput) return;
    this.workspace.sendScreenKeys(['enter'], this.screenInput, this.workspace.screenRevision);
    this.screenInput = '';
  }

  @Builder fileMenu() {
    Menu() {
      MenuItem({ content: tr('refresh') }).onClick(() => this.workspace.loadFiles())
      if (this.workspace.canUploadFiles()) MenuItem({ content: tr('upload') }).enabled(!this.pickerBusy).onClick(() => this.pickUpload())
      if (this.workspace.trashUndo.length) MenuItem({ content: tr('session_tools_restore') }).enabled(this.workspace.canEditFiles())
        .onClick(() => this.restoreTrash())
      MenuItem({ content: tr('parent_folder') }).enabled(this.workspace.currentPath !== this.workspace.cwd)
        .onClick(() => { this.workspace.openParent(); this.pathDraft = this.workspace.currentPath; })
    }
  }

  @Builder fileEntryMenu(binding: BoundRow<WorkspaceEntry>) {
    Menu() {
      MenuItem({ content: tr('open') }).onClick(() => this.openEntry(binding.value))
      if (this.workspace.canSendFile(binding.value)) MenuItem({ content: tr('file_transfer_send_to_computer') })
        .onClick(() => this.openFileTransfer(binding.value))
      if (this.workspace.canEditFiles()) MenuItem({ content: tr('rename') }).onClick(() => this.openFileActions('rename', binding.value))
      if (this.workspace.canEditFiles()) {
        MenuItem({ content: tr('session_tools_move') }).onClick(() => this.openFileActions('move', binding.value))
        MenuItem({ content: tr('session_tools_move_to_trash') }).onClick(() => this.confirmTrash(binding.value))
      }
    }
  }

  @Builder gitMenu() {
    Menu() {
      MenuItem({ content: tr('refresh') }).onClick(() => this.workspace.loadGit())
      if (this.model.features.includes('git_write')) {
        MenuItem({ content: tr('session_tools_commit') }).enabled(this.workspace.canWriteGit() && this.workspace.gitFiles.some((file: WorkspaceGitFile) => file.staged))
          .onClick(() => this.openGitSheet('commit'))
        MenuItem({ content: tr('session_tools_push') }).enabled(this.workspace.canWriteGit() && !!Wire.text(this.workspace.gitStatus, 'upstream'))
          .onClick(() => this.confirmGitWrite('push'))
        MenuItem({ content: tr('session_tools_publish_branch') }).enabled(this.workspace.canWriteGit() && !Wire.text(this.workspace.gitStatus, 'upstream'))
          .onClick(() => this.confirmGitWrite('publish'))
        MenuItem({ content: tr('session_tools_switch_branch') }).enabled(this.workspace.canWriteGit())
          .onClick(() => this.openGitSheet('branch'))
        MenuItem({ content: tr('session_tools_stash_save') }).enabled(this.workspace.canWriteGit() && this.workspace.gitFiles.length > 0)
          .onClick(() => this.openGitSheet('stash_push'))
      }
    }
  }

  @Builder gitFileMenu(binding: BoundRow<WorkspaceGitFile>) {
    Menu() {
      MenuItem({ content: tr('session_tools_stage') }).enabled(this.workspace.canWriteGit() && (binding.value.untracked || binding.value.unstaged))
        .onClick(() => this.confirmGitWrite('stage', binding.value.path))
      MenuItem({ content: tr('session_tools_unstage') }).enabled(this.workspace.canWriteGit() && binding.value.staged)
        .onClick(() => this.confirmGitWrite('unstage', binding.value.path))
    }
  }

  @Builder gitStashMenu(binding: BoundRow<Record<string, Object>>) {
    Menu() {
      MenuItem({ content: tr('session_tools_stash_apply') }).enabled(this.workspace.canWriteGit() && !!Wire.text(binding.value, 'oid'))
        .onClick(() => this.confirmGitWrite('stash_apply', Wire.text(binding.value, 'oid')))
      MenuItem({ content: tr('session_tools_stash_pop') }).enabled(this.workspace.canWriteGit() && !!Wire.text(binding.value, 'oid'))
        .onClick(() => this.confirmGitWrite('stash_pop', Wire.text(binding.value, 'oid')))
      MenuItem({ content: tr('session_tools_stash_drop') }).enabled(this.workspace.canWriteGit() && !!Wire.text(binding.value, 'oid'))
        .onClick(() => this.confirmGitWrite('stash_drop', Wire.text(binding.value, 'oid')))
    }
  }

  @Builder fileRow(binding: BoundRow<WorkspaceEntry>) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.name,
      secondaryText: binding.value.directory ? tr('directory') : [binding.value.symlink ? tr('symlink') : '',
        binding.value.ignored ? tr('ignored') : '', this.sizeText(binding.value.size)].filter((value: string) => !!value).join(' · ') }) })
  }

  @Builder fileView() {
    Column() {
      Row({ space: Tokens.gap }) {
        Button({ buttonStyle: ButtonStyleMode.TEXTUAL }) {
          Image($r('sys.media.ohos_ic_public_arrow_up')).width(Tokens.title).height(Tokens.title).fillColor($r('sys.color.icon_primary'))
        }.enabled(this.workspace.currentPath !== this.workspace.cwd).accessibilityText(tr('session_tools_parent_folder'))
          .onClick(() => { this.workspace.openParent(); this.pathDraft = this.workspace.currentPath; }).width(Tokens.touch).height(Tokens.touch).id('session.files.parent')
        TextInput({ text: this.pathDraft, placeholder: tr('path') }).onChange((value: string) => this.pathDraft = value)
          .layoutWeight(1).constraintSize({ minHeight: Tokens.touch }).id('session.files.path')
        Button({ buttonStyle: ButtonStyleMode.TEXTUAL }) {
          Image($r('sys.media.ohos_ic_public_arrow_right')).width(Tokens.title).height(Tokens.title).fillColor($r('sys.color.icon_primary'))
        }.onClick(() => this.openPath()).accessibilityText(tr('open')).width(Tokens.touch).height(Tokens.touch).id('session.files.open')
        Button({ buttonStyle: ButtonStyleMode.TEXTUAL }) {
          Image($r('sys.media.ohos_ic_public_more')).width(Tokens.title).height(Tokens.title).fillColor($r('sys.color.icon_primary'))
        }.bindMenu(this.fileMenu).accessibilityText(tr('more')).width(Tokens.touch).height(Tokens.touch).id('session.files.more')
      }.padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.inset, bottom: Tokens.gap })

      if (this.workspace.trashUndo.length && this.workspace.canEditFiles()) Row({ space: Tokens.gap }) {
        Text(tr('session_tools_trashed_count').replace('{count}', String(this.workspace.trashUndo.length))).fontSize(Tokens.meta).layoutWeight(1)
        Button(tr('session_tools_restore')).onClick(() => this.restoreTrash()).height(Tokens.touch)
      }.padding({ left: Tokens.inset, right: Tokens.inset, bottom: Tokens.gap })

      if (this.transferError) Text(this.transferError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (this.workspace.fileError) Text(this.workspace.fileError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (['pending', 'unknown'].includes(this.workspace.fileWriteState)) Text(tr('session_tools_write_' + this.workspace.fileWriteState))
        .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (this.pickerBusy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
      if (this.workspace.fileBusy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
      List() {
        ForEach(this.fileRowBindings, (binding: BoundRow<WorkspaceEntry>) => {
          ListItem() { this.fileRow(binding) }.onClick(() => this.openEntry(binding.value))
            .bindContextMenu(this.fileEntryMenu(binding), ResponseType.LongPress).id('session.files.' + binding.value.path)
        }, (binding: BoundRow<WorkspaceEntry>) => binding.key)
        if (!this.workspace.entries.length && !this.workspace.fileBusy && !this.workspace.fileError)
          ListItem() { Text(tr('empty')).padding(Tokens.page) }
        if (this.workspace.hasMoreEntries) ListItem() { Text(tr('session_tools_first_entries')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
    }.width('100%').height('100%')
      .bindSheet($$this.fileOpen, this.fileViewer(), {
        title: { title: this.workspace.selectedFileName || tr('file') }, height: SheetSize.LARGE, showClose: true
      })
      .bindSheet($$this.fileEditSheetOpen, this.fileEditForm(), {
        title: { title: tr(this.fileEditMode === 'rename' ? 'session_tools_rename' : 'session_tools_move') }, height: SheetSize.MEDIUM, showClose: true
      })
      .bindSheet($$this.transferOpen, this.fileTransferForm(), {
        title: { title: tr('file_transfer_title') }, height: SheetSize.LARGE, showClose: true
      })
  }

  private transferSetting(source: boolean): FileTransferSetting {
    return source ? this.workspace.fileTransfer.sourceSetting : this.workspace.fileTransfer.targetSetting;
  }

  private transferSettingLabel(source: boolean): string {
    return tr('file_transfer_shared_switch') + ' · ' + (source ? this.workspace.fileTransfer.sourceMachineName() : this.workspace.fileTransfer.targetMachineName());
  }

  @Builder transferSettingRow(source: boolean) {
    Column({ space: Tokens.fine }) {
      Row({ space: Tokens.gap }) {
        Text(this.transferSettingLabel(source)).fontSize(Tokens.body).layoutWeight(1)
        Toggle({ type: ToggleType.Switch, isOn: this.transferSetting(source).enabled })
          .enabled(this.transferSetting(source).state === 'ready' && this.workspace.fileTransfer.isAvailable() && !this.workspace.fileTransfer.createBusy)
          .accessibilityText(this.transferSettingLabel(source))
          .onChange((enabled: boolean) => this.workspace.fileTransfer.setEnabled(this.transferSetting(source).machineId, enabled))
      }.width('100%')
      if (this.transferSetting(source).state === 'loading' || this.transferSetting(source).state === 'saving') Row({ space: Tokens.gap }) {
        LoadingProgress().width(Tokens.touch).height(Tokens.touch)
        Text(tr('file_transfer_setting_' + this.transferSetting(source).state)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      }
      if (this.transferSetting(source).state === 'unknown') Text(tr('file_transfer_setting_unknown') + (this.transferSetting(source).error ? ': ' + this.transferSetting(source).error : ''))
        .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.transferSetting(source).state === 'ready' && this.transferSetting(source).error) Text(tr('file_transfer_error') + ': ' + this.transferSetting(source).error)
        .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.transferSetting(source).state === 'ready' && this.transferSetting(source).inbox) Text(tr('file_transfer_inbox') + ': ' + this.transferSetting(source).inbox)
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      else if (this.transferSetting(source).state === 'ready' && !this.transferSetting(source).enabled) Text(tr('file_transfer_inbox_enable_hint'))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      else if (this.transferSetting(source).state === 'ready' && this.transferSetting(source).enabled && !this.transferSetting(source).inbox) Text(tr('file_transfer_inbox_unknown'))
        .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
    }.width('100%').padding({ top: Tokens.gap, bottom: Tokens.gap })
  }

  @Builder transferTargetRow(machine: FileTransferMachine) {
    Row({ space: Tokens.gap }) {
      Radio({ value: machine.id, group: 'session-file-transfer-target' })
        .checked(this.workspace.fileTransfer.targetId === machine.id)
        .enabled(!this.workspace.fileTransfer.targetSelectionFrozen())
        .accessibilityText(machine.title)
        .onChange((checked: boolean) => { if (checked && !this.workspace.fileTransfer.targetSelectionFrozen()) this.selectTransferTarget(machine.id); })
      Text(machine.title).fontSize(Tokens.body).layoutWeight(1)
    }.width('100%').padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.fine, bottom: Tokens.fine })
      .enabled(!this.workspace.fileTransfer.targetSelectionFrozen())
      .opacity(this.workspace.fileTransfer.targetSelectionFrozen() ? 0.55 : 1)
      .onClick(() => { if (!this.workspace.fileTransfer.targetSelectionFrozen()) this.selectTransferTarget(machine.id); })
  }

  @Builder transferUnknownRow(record: FileTransferRecord) {
    Column({ space: Tokens.fine }) {
      Text(tr('file_transfer_status_unknown') + ' · ' + record.targetName).fontSize(Tokens.body).width('100%')
      Flex({ wrap: FlexWrap.Wrap, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
        if (record.id) Button(tr('file_transfer_check_status')).flexGrow(1).flexShrink(0).constraintSize({ minHeight: Tokens.touch }).onClick(() => this.checkTransfer(record.key))
        if (!record.acknowledged) Button(tr('file_transfer_i_checked')).flexGrow(1).flexShrink(0).constraintSize({ minHeight: Tokens.touch }).onClick(() => this.acknowledgeTransfer(record.key))
      }.width('100%')
      if (record.code) Text(tr('file_transfer_error') + ': ' + record.code).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (record.acknowledged) Text(tr('file_transfer_possible_duplicate')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
    }.width('100%').padding({ top: Tokens.gap, bottom: Tokens.gap })
  }

  @Builder fileTransferForm() {
    Column({ space: Tokens.gap }) {
      Scroll() {
        Column({ space: Tokens.gap }) {
      Text(this.workspace.fileTransfer.sourceFileName()).fontSize(Tokens.title)
      Text(this.workspace.fileTransfer.sourceMachineName() + ' · ' + this.workspace.fileTransfer.sourceFilePath())
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      this.transferSettingRow(true)
      Text(tr('file_transfer_choose_target')).fontSize(Tokens.body).fontWeight(FontWeight.Medium)
      if (!this.workspace.fileTransfer.targets.length) Text(tr('file_transfer_no_targets')).fontSize(Tokens.meta)
      List() {
        ForEach(this.workspace.fileTransfer.targets, (machine: FileTransferMachine) => {
          ListItem() { this.transferTargetRow(machine) }.id('session.file-transfer.target.' + machine.id)
        }, (machine: FileTransferMachine) => machine.id)
      }.width('100%').height(180)
        .nestedScroll({ scrollForward: NestedScrollMode.SELF_FIRST, scrollBackward: NestedScrollMode.SELF_FIRST })
      if (this.workspace.fileTransfer.targetId && this.workspace.fileTransfer.targetId !== this.workspace.currentMachineId())
        this.transferSettingRow(false)
      if (this.currentTransfer()) {
        if (this.workspace.fileTransfer.targetId !== this.currentTransfer()?.targetMachine) {
          Text(this.currentTransfer()?.targetName || this.currentTransfer()?.targetMachine || '').fontSize(Tokens.body)
          if (this.currentTransfer()?.targetInbox) Text(this.currentTransfer()?.targetInbox || '').fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        }
        if (this.currentTransfer()?.state === 'running') Progress({ value: this.currentTransferProgress(), total: 100, type: ProgressType.Linear }).width('100%')
        Text(this.currentTransferStatus()).fontSize(Tokens.body)
        if (this.currentTransfer()?.resultPath) Text(this.currentTransfer()?.resultPath || '').fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        if (this.currentTransfer()?.code) Text(tr('file_transfer_error') + ': ' + (this.currentTransfer()?.code || ''))
          .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
        if (this.currentTransfer()?.cancelUnknown) Text(tr('file_transfer_cancel_unknown')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
        if (this.currentTransfer()?.state === 'running' && this.currentTransfer()?.id) Button(tr('file_transfer_cancel_transfer'))
          .enabled(!this.currentTransfer()?.cancelBusy).constraintSize({ minHeight: Tokens.touch }).onClick(() => this.cancelTransfer(this.currentTransferCancelKey()))
      }
      ForEach(this.workspace.fileTransfer.unknownForSource(), (record: FileTransferRecord) => {
        this.transferUnknownRow(record)
      }, (record: FileTransferRecord) => record.key)
      if (this.transferError) Text(this.transferError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.workspace.fileTransfer.createBusy) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.workspace.fileTransfer.journalError) Text(tr('file_transfer_journal_unavailable'))
        .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
        }.width('100%').alignItems(HorizontalAlign.Start)
      }.width('100%').layoutWeight(1).align(Alignment.TopStart).scrollBar(BarState.Auto)
      Flex({ wrap: FlexWrap.Wrap, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
        Button(tr('cancel')).flexGrow(1).flexShrink(0).constraintSize({ minHeight: Tokens.touch }).onClick(() => this.transferOpen = false)
        Button(tr('file_transfer_confirm_send')).flexGrow(1).flexShrink(0).constraintSize({ minHeight: Tokens.touch })
          .enabled(this.workspace.fileTransfer.canStart() && !this.workspace.fileTransfer.createBusy)
          .onClick(() => this.confirmFileTransfer()).id('session.files.transfer.confirm')
      }.width('100%')
    }.padding(Tokens.page).width('100%').height('100%')
  }

  @Builder fileViewer() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Button(tr('download')).enabled(!!this.workspace.selectedFile).onClick(() => this.saveDownload()).height(Tokens.touch)
        if (!this.workspace.selectedFileIsText && this.workspace.selectedFile && this.model.features.includes('file_download'))
          Button(tr('session_tools_preview_file')).enabled(!this.previewBusy && !this.pickerBusy)
            .onClick(() => this.previewSelectedFile()).height(Tokens.touch)
      }.width('100%').justifyContent(FlexAlign.End)
      Text(this.workspace.selectedFile + ' · ' + this.sizeText(this.workspace.selectedFileSize) +
        (this.workspace.selectedFileMime ? ' · ' + this.workspace.selectedFileMime : ''))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(2)
      if (this.workspace.fileError) Text(this.workspace.fileError).fontColor($r('app.color.persona_danger'))
      if (this.transferError) Text(this.transferError).fontColor($r('app.color.persona_danger'))
      if (this.workspace.fileReadBusy) LoadingProgress().layoutWeight(1).width(Tokens.touch).height(Tokens.touch)
      else if (this.workspace.selectedFileIsText) {
        Scroll() {
          Text(this.workspace.selectedFileText).fontFamily('monospace').fontSize(Tokens.meta).copyOption(CopyOptions.LocalDevice)
            .width('100%').textAlign(TextAlign.Start)
        }.layoutWeight(1).width('100%')
        if (this.workspace.fileHasMore) Button(tr('load_more')).onClick(() => this.workspace.loadMoreText()).height(Tokens.touch)
        if (this.workspace.fileTruncated && !this.workspace.fileHasMore) Text(tr('session_tools_truncated')).fontSize(Tokens.meta)
      } else {
        Column({ space: Tokens.gap }) {
          Text(tr('session_tools_binary_file')).fontSize(Tokens.body)
          Text(this.workspace.selectedFileMime).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        }.layoutWeight(1).justifyContent(FlexAlign.Center)
      }
    }.padding(Tokens.page).width('100%').height('100%')
  }

  @Builder fileEditForm() {
    Column({ space: Tokens.gap }) {
      if (this.fileActionEntry) Text(this.fileActionEntry.path).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(2)
      if (this.fileEditMode === 'rename') TextInput({ text: this.renameDraft, placeholder: tr('session_tools_new_name') })
        .onChange((value: string) => this.renameDraft = value).id('session.files.rename')
      else TextInput({ text: this.moveTarget, placeholder: tr('session_tools_destination_folder') })
        .onChange((value: string) => this.moveTarget = value).id('session.files.move')
      if (this.fileEditMode === 'move_conflict') {
        Text(tr('session_tools_move_conflict') + ' · ' + this.workspace.fileConflictName).fontSize(Tokens.meta)
        Select([{ value: tr('session_tools_conflict_keep') }, { value: tr('session_tools_conflict_replace') }])
          .selected(this.moveConflictChoice).value(tr(this.moveConflictChoice === 0 ? 'session_tools_conflict_keep' : 'session_tools_conflict_replace'))
          .onSelect((index: number) => this.moveConflictChoice = index === 1 ? 1 : 0).id('session.files.conflict')
      }
      if (['pending', 'unknown'].includes(this.workspace.fileWriteState)) Text(tr('session_tools_write_' + this.workspace.fileWriteState))
        .fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.workspace.fileWriteError) Text(this.workspace.fileWriteError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      Row({ space: Tokens.gap }) {
        Button(tr('cancel')).layoutWeight(1).height(Tokens.touch).onClick(() => this.fileEditSheetOpen = false)
        Button(tr('continue')).layoutWeight(1).height(Tokens.touch)
          .enabled(this.workspace.canEditFiles() && !this.fileEditBusy && !['pending', 'unknown'].includes(this.workspace.fileWriteState))
          .onClick(() => this.confirmFileEdit()).id('session.files.edit.confirm')
      }.width('100%')
      if (this.fileEditBusy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
    }.padding(Tokens.page).width('100%')
  }

  @Builder gitView() {
    Column() {
      Row({ space: Tokens.gap }) {
        Text(tr('git')).fontSize(Tokens.title).layoutWeight(1)
        Button(tr('refresh')).onClick(() => this.workspace.loadGit()).height(Tokens.touch)
        Button(tr('more')).bindMenu(this.gitMenu).height(Tokens.touch)
      }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.gap })
      if (this.workspace.gitError) Text(this.workspace.gitError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (this.workspace.gitBusy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
      if (Object.keys(this.workspace.gitStatus).length > 0 && !this.workspace.gitBusy) {
        if (!Wire.bool(this.workspace.gitStatus, 'is_repo')) {
          Column({ space: Tokens.gap }) { Text(tr('session_tools_not_repository')).fontSize(Tokens.body); Text(this.workspace.cwd).fontSize(Tokens.meta) }
            .alignItems(HorizontalAlign.Start).padding(Tokens.page).width('100%')
        } else {
          List() {
            ListItem() {
              Column({ space: Tokens.gap }) {
                Text(Wire.text(this.workspace.gitStatus, 'branch', tr('branch'))).fontSize(Tokens.title)
                Text(Wire.text(this.workspace.gitStatus, 'upstream', tr('session_tools_no_upstream'))).fontSize(Tokens.meta)
                  .fontColor($r('sys.color.font_secondary'))
                Row({ space: Tokens.gap }) {
                  if (Wire.number(this.workspace.gitStatus, 'ahead') > 0) Text('↑ ' + String(Wire.number(this.workspace.gitStatus, 'ahead')))
                    .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
                  if (Wire.number(this.workspace.gitStatus, 'behind') > 0) Text('↓ ' + String(Wire.number(this.workspace.gitStatus, 'behind')))
                    .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
                  if (Wire.number(this.workspace.gitStatus, 'ahead') === 0 && Wire.number(this.workspace.gitStatus, 'behind') === 0 && Wire.text(this.workspace.gitStatus, 'upstream')) Text(tr('session_tools_synced'))
                    .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
                }
              }.alignItems(HorizontalAlign.Start).padding(Tokens.page).width('100%')
            }
            ListItem() { Text(tr('session_tools_changes') + ' · ' + String(this.workspace.gitFiles.length)).fontSize(Tokens.body).padding(Tokens.inset) }
            ForEach(this.gitRowBindings, (binding: BoundRow<WorkspaceGitFile>) => {
              ListItem() {
                Row({ space: Tokens.gap }) {
                  Text(binding.value.mark).fontSize(Tokens.body).fontWeight(FontWeight.Bold).width(24).textAlign(TextAlign.Center)
                  Column({ space: Tokens.fine }) {
                    Text(binding.value.path).fontSize(Tokens.body).maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis })
                    Row({ space: Tokens.gap }) {
                      if (binding.value.originalPath) Text('← ' + binding.value.originalPath).fontSize(Tokens.meta)
                        .fontColor($r('sys.color.font_secondary'))
                      if (binding.value.binary) Text(tr('session_tools_binary_file')).fontSize(Tokens.meta)
                        .fontColor($r('sys.color.font_secondary'))
                      else if (binding.value.added + binding.value.deleted > 0) Text('+' + String(binding.value.added) + '  −' + String(binding.value.deleted)).fontSize(Tokens.meta)
                        .fontColor($r('sys.color.font_secondary'))
                    }
                  }.alignItems(HorizontalAlign.Start).layoutWeight(1)
                  Text('›').fontSize(Tokens.title).fontColor($r('sys.color.font_secondary'))
                }.height(Tokens.touch + Tokens.inset).padding({ left: Tokens.inset, right: Tokens.inset }).width('100%')
              }.onClick(() => this.openGitFile(binding.value))
                .bindContextMenu(this.gitFileMenu(binding), ResponseType.LongPress).id('session.git.' + binding.value.path)
            }, (binding: BoundRow<WorkspaceGitFile>) => binding.key)
            if (!this.workspace.gitFiles.length) ListItem() { Text(tr('session_tools_clean')).padding(Tokens.page) }
            if (this.workspace.gitStashes.length) ListItem() { Text(tr('session_tools_stashes')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).padding(Tokens.inset) }
            ForEach(this.stashRowBindings, (binding: BoundRow<Record<string, Object>>) => {
              ListItem() {
                Row({ space: Tokens.gap }) {
                  Text('stash@{' + String(Wire.number(binding.value, 'index')) + '}').fontSize(Tokens.meta).fontFamily('monospace')
                  Text(Wire.text(binding.value, 'message')).fontSize(Tokens.meta).layoutWeight(1).maxLines(2)
                }.height(Tokens.touch).padding({ left: Tokens.inset, right: Tokens.inset }).width('100%')
              }.bindContextMenu(this.gitStashMenu(binding), ResponseType.LongPress)
                .id('session.git.stash.' + Wire.text(binding.value, 'oid'))
            }, (binding: BoundRow<Record<string, Object>>) => binding.key)
          }.width('100%').layoutWeight(1)
        }
      } else if (!this.workspace.gitBusy && !this.workspace.gitError) {
        Text(tr('loading')).padding(Tokens.page)
      }
    }.width('100%').height('100%')
      .bindSheet($$this.diffOpen, this.diffViewer(), {
        title: { title: this.workspace.gitDiffPath.split('/').pop() || tr('git') }, height: SheetSize.LARGE, showClose: true
      })
      .bindSheet($$this.gitSheetOpen, this.gitSheet(), {
        title: { title: tr(this.gitSheetMode === 'commit' ? 'session_tools_commit' : this.gitSheetMode === 'branch' ? 'session_tools_switch_branch' : 'session_tools_stash_save') }, height: SheetSize.MEDIUM, showClose: true
      })
  }

  private branchOptions(): SelectOption[] { return this.workspace.gitBranches.map((branch: WorkspaceBranch): SelectOption => ({ value: this.branchLabel(branch) })); }
  @Builder gitSheet() {
    Column({ space: Tokens.gap }) {
      if (this.gitSheetMode === 'commit') {
        TextArea({ text: this.gitDraftMessage, placeholder: tr('session_tools_commit_message') }).height(Tokens.inputExpanded)
          .onChange((value: string) => this.gitDraftMessage = value).id('session.git.commit_message')
        Button(tr('session_tools_commit')).enabled(this.workspace.canWriteGit() && !!this.gitDraftMessage.trim() && !this.workspace.gitWriteBusy)
          .height(Tokens.touch).width('100%').onClick(() => this.confirmGitWrite('commit')).id('session.git.commit')
      } else if (this.gitSheetMode === 'branch') {
        if (this.workspace.gitBranches.length) {
          Select(this.branchOptions()).selected(Math.max(0, this.gitSelectedBranch)).value(this.branchLabel(this.workspace.gitBranches[this.gitSelectedBranch] || this.workspace.gitBranches[0]))
            .onSelect((index: number) => this.gitSelectedBranch = index).id('session.git.branch')
          Button(tr('session_tools_switch_branch')).enabled(this.workspace.canWriteGit() && !this.workspace.gitBranches[this.gitSelectedBranch]?.current)
            .height(Tokens.touch).width('100%').onClick(() => this.confirmGitWrite('switch')).id('session.git.switch')
        } else Text(tr('session_tools_no_branches')).fontSize(Tokens.meta)
        TextInput({ text: this.gitNewBranchName, placeholder: tr('session_tools_new_branch') })
          .onChange((value: string) => this.gitNewBranchName = value).id('session.git.new_branch')
        Button(tr('session_tools_create_branch')).enabled(this.workspace.canWriteGit() && !!this.gitNewBranchName.trim())
          .height(Tokens.touch).width('100%').onClick(() => this.confirmGitWrite('create')).id('session.git.create_branch')
      } else if (this.gitSheetMode === 'stash_push') {
        TextInput({ text: this.gitStashMessage, placeholder: tr('session_tools_stash_message') })
          .onChange((value: string) => this.gitStashMessage = value).id('session.git.stash_message')
        Row({ space: Tokens.gap }) {
          Text(tr('session_tools_stash_untracked')).layoutWeight(1).fontSize(Tokens.body)
          Toggle({ type: ToggleType.Switch, isOn: this.gitStashUntracked }).onChange((value: boolean) => this.gitStashUntracked = value)
            .accessibilityText(tr('session_tools_stash_untracked')).id('session.git.stash_untracked')
        }.width('100%')
        Button(tr('session_tools_stash_save')).enabled(this.workspace.canWriteGit() && !this.workspace.gitWriteBusy)
          .height(Tokens.touch).width('100%').onClick(() => this.confirmGitWrite('stash_push')).id('session.git.stash_save')
      }
      if (this.workspace.gitWriteState === 'pending' || this.workspace.gitWriteState === 'unknown')
        Text(tr('session_tools_write_' + this.workspace.gitWriteState)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.workspace.gitWriteError) Text(this.workspace.gitWriteError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.workspace.gitWriteBusy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
      Button(tr('cancel')).height(Tokens.touch).width('100%').onClick(() => this.gitSheetOpen = false)
    }.alignItems(HorizontalAlign.Start).padding(Tokens.page).width('100%')
  }

  @Builder diffViewer() {
    Column({ space: Tokens.gap }) {
      Text(this.workspace.gitDiffPath).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (this.gitDiffHasBoth()) Row({ space: Tokens.gap }) {
        Button(tr('session_tools_unstaged')).enabled(this.workspace.gitDiffStaged).onClick(() => { this.workspace.gitDiffStaged = false; this.workspace.loadGitDiff(); })
        Button(tr('session_tools_staged')).enabled(!this.workspace.gitDiffStaged).onClick(() => { this.workspace.gitDiffStaged = true; this.workspace.loadGitDiff(); })
      }
      if (this.workspace.gitDiffError) Text(this.workspace.gitDiffError).fontColor($r('app.color.persona_danger'))
      if (this.workspace.gitDiffBusy) LoadingProgress().layoutWeight(1).width(Tokens.touch).height(Tokens.touch)
      else if (this.workspace.gitDiff) Scroll() {
        Text(this.workspace.gitDiff).fontFamily('monospace').fontSize(Tokens.meta).copyOption(CopyOptions.LocalDevice)
          .width('100%').textAlign(TextAlign.Start)
      }.layoutWeight(1).width('100%')
      else if (!this.workspace.gitDiffError) Text(tr('session_tools_no_diff')).fontSize(Tokens.meta).layoutWeight(1)
      if (this.workspace.gitDiffTruncated) Text(tr('session_tools_truncated')).fontSize(Tokens.meta)
    }.padding(Tokens.page).width('100%').height('100%')
  }

  @Builder terminalView() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(tr('terminal')).fontSize(Tokens.title).layoutWeight(1)
        if (this.workspace.terminalControlState === 'active') Button(tr('release')).onClick(() => this.workspace.setTerminalControl(false)).height(Tokens.touch)
        else if (this.workspace.terminalControlState === 'busy') Button(tr('takeover')).enabled(this.workspace.canControlTerminal())
          .onClick(() => this.confirmTerminalControl(true)).height(Tokens.touch)
        else Button(tr('control')).enabled(this.workspace.canControlTerminal()).onClick(() => this.confirmTerminalControl()).height(Tokens.touch)
      }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.gap })
      Text(this.controlDescription()).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).padding({ left: Tokens.page, right: Tokens.page })
      if (this.workspace.terminalError) Text(this.workspace.terminalError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      Scroll() {
        Text(this.workspace.terminalText || tr('session_tools_terminal_waiting'))
          .fontFamily('monospace').fontSize(Tokens.meta).width('100%').textAlign(TextAlign.Start).copyOption(CopyOptions.LocalDevice)
      }.layoutWeight(1).width('100%').onAreaChange((oldArea: Area, newArea: Area) => this.measureTerminal(newArea))
      if (this.workspace.terminalControlState === 'active') {
        TextArea({ text: this.terminalInput, placeholder: tr('session_tools_terminal_input') })
          .height(Tokens.inputExpanded / 2).onChange((value: string) => this.terminalInput = value)
        Row({ space: Tokens.gap }) {
          Button('Esc').onClick(() => this.workspace.sendTerminalKey('escape'))
          Button('Tab').onClick(() => this.workspace.sendTerminalKey('tab'))
          Button('↑').onClick(() => this.workspace.sendTerminalKey('up'))
          Button('↓').onClick(() => this.workspace.sendTerminalKey('down'))
          Button('Ctrl-C').onClick(() => this.workspace.sendTerminalKey('ctrl_c'))
          Button(tr('send')).layoutWeight(1).onClick(() => this.sendTerminalInput()).height(Tokens.touch)
        }.padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.inset })
      }
    }.width('100%').height('100%')
  }

  @Builder terminalScreenView() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(tr('session_tools_terminal_screen')).fontSize(Tokens.title).layoutWeight(1)
        Button(tr('refresh')).onClick(() => this.workspace.refreshScreen()).height(Tokens.touch)
        if (this.workspace.screenControlRequested) Button(tr('release')).onClick(() => this.workspace.revokeScreenControl()).height(Tokens.touch)
        else Button(tr('control')).enabled(this.workspace.canControlScreen()).onClick(() => this.confirmScreenControl()).height(Tokens.touch)
      }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.gap })
      Text(tr(this.workspace.screenControlRequested ? 'session_tools_terminal_screen_control_active' : 'session_tools_terminal_screen_view_only'))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).padding({ left: Tokens.page, right: Tokens.page })
      if (this.workspace.screenError) Text(tr(this.workspace.screenError)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (this.transferError) Text(this.transferError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      Scroll() {
        Text(this.workspace.screenText || tr('session_tools_terminal_waiting'))
          .fontFamily('monospace').fontSize(Tokens.meta).width('100%').textAlign(TextAlign.Start).copyOption(CopyOptions.LocalDevice)
      }.layoutWeight(1).width('100%')
      if (this.workspace.screenControlRequested) {
        TextInput({ text: this.screenInput, placeholder: tr('session_tools_terminal_screen_input') })
          .height(Tokens.touch).onChange((value: string) => this.screenInput = value)
        Row({ space: Tokens.gap }) {
          Button('Esc').onClick(() => this.sendTerminalScreenKey('esc'))
          Button('Tab').onClick(() => this.sendTerminalScreenKey('tab'))
          Button('↑').onClick(() => this.sendTerminalScreenKey('up'))
          Button('↓').onClick(() => this.sendTerminalScreenKey('down'))
          Button('Ctrl-C').onClick(() => this.sendTerminalScreenKey('ctrl+c'))
          Button(tr('send')).layoutWeight(1).onClick(() => this.sendTerminalScreenText()).height(Tokens.touch)
        }.padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.inset })
      }
    }.width('100%').height('100%')
  }

  @Builder previewServerRow(binding: BoundRow<WorkspacePreviewServer>) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(binding.value.name).fontSize(Tokens.body).fontWeight(FontWeight.Medium).layoutWeight(1).maxLines(1)
        Text(binding.value.listening ? tr('session_tools_preview_listening') : tr('session_tools_preview_stopped'))
          .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      }
      if (binding.value.title) Text(binding.value.title).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(2)
      if (binding.value.problem) Text(tr('session_tools_preview_problem') + ': ' + binding.value.problem).fontSize(Tokens.meta)
        .fontColor($r('app.color.persona_danger'))
      if (binding.value.command.length) Text(this.previewCommand(binding.value)).fontSize(Tokens.meta).fontFamily('monospace').maxLines(3)
        .textOverflow({ overflow: TextOverflow.Ellipsis })
      Text(binding.value.cwd || this.workspace.cwd).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(2)
      if (binding.value.listening && binding.value.port > 0) Button(tr('session_tools_preview_open')).enabled(this.workspace.canUsePreview())
        .height(Tokens.touch).onClick(() => this.openReadyPreview(binding.value)).width('100%')
      else if (binding.value.command.length) Button(tr('start')).enabled(this.workspace.canStartPreview() && !binding.value.problem)
        .height(Tokens.touch).onClick(() => this.confirmStartPreview(binding.value)).width('100%')
    }.alignItems(HorizontalAlign.Start).padding(Tokens.inset).width('100%')
  }

  @Builder previewView() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(tr('preview')).fontSize(Tokens.title).layoutWeight(1)
        Button(tr('refresh')).onClick(() => this.workspace.loadPreviewServers()).height(Tokens.touch)
        if (this.workspace.previewState === 'live') {
          Button(tr('back')).onClick(() => this.previewAction('back')).height(Tokens.touch)
          Button(tr('session_tools_forward')).onClick(() => this.previewAction('forward')).height(Tokens.touch)
          Button(tr('refresh')).onClick(() => this.previewAction('reload')).height(Tokens.touch)
        }
      }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.gap })
      if (this.workspace.previewError) Text(tr(this.workspace.previewError)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (this.transferError) Text(this.transferError).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (this.workspace.previewState === 'starting' || this.workspace.previewState === 'opening' || this.workspace.previewState === 'connecting') LoadingProgress()
        .height(Tokens.touch).width(Tokens.touch)
      if (this.workspace.previewMessage) Text(this.workspace.previewMessage).fontSize(Tokens.meta).maxLines(1)
        .fontColor($r('sys.color.font_secondary')).padding({ left: Tokens.inset, right: Tokens.inset })
      if (this.workspace.previewState === 'live' && this.previewPixels) {
        Row({ space: Tokens.gap }) {
          TextInput({ text: this.previewPathDraft, placeholder: tr('session_tools_preview_path') }).layoutWeight(1)
            .onChange((value: string) => this.previewPathDraft = value).height(Tokens.touch)
          Button(tr('open')).onClick(() => this.sendPreviewNavigation()).height(Tokens.touch)
        }.padding({ left: Tokens.inset, right: Tokens.inset })
        Image(this.previewPixels).objectFit(ImageFit.Contain).aspectRatio(this.previewRatio).width('100%').layoutWeight(1)
        Row({ space: Tokens.gap }) {
          TextInput({ text: this.previewInput, placeholder: tr('session_tools_preview_input') }).layoutWeight(1)
            .onChange((value: string) => this.previewInput = value).height(Tokens.touch)
          Button(tr('send')).onClick(() => this.sendPreviewText()).height(Tokens.touch)
        }.padding({ left: Tokens.inset, right: Tokens.inset })
        if (this.workspace.previewExternalUrl) Button(tr('session_tools_preview_external_link'))
          .onClick(() => this.openPreviewExternal()).height(Tokens.touch).width('100%')
      } else {
        List() {
          ForEach(this.previewRowBindings, (binding: BoundRow<WorkspacePreviewServer>) => ListItem() { this.previewServerRow(binding) }
            .id('session.preview.' + binding.value.name), (binding: BoundRow<WorkspacePreviewServer>) => binding.key)
          if (!this.workspace.previewServers.length && !this.workspace.previewError && this.workspace.previewState !== 'unsupported')
            ListItem() { Text(tr('session_tools_preview_none')).fontSize(Tokens.meta).padding(Tokens.page) }
        }.layoutWeight(1).width('100%')
      }
    }.width('100%').height('100%')
  }

  private sendTerminalScreenKey(key: string): void {
    const revision = this.workspace.screenRevision;
    this.workspace.sendScreenKeys([key], '', revision).then((ok: boolean) => {
      if (!ok && this.workspace.screenError) this.transferError = tr(this.workspace.screenError);
    });
  }

  private controlDescription(): string {
    const state = this.workspace.terminalControlState;
    if (state === 'active') return tr('session_tools_control_active');
    if (state === 'starting') return tr('session_tools_control_starting');
    if (state === 'busy') return tr('session_tools_control_busy');
    if (state === 'unsupported') return tr('session_tools_view_only');
    return tr('session_tools_control_off');
  }

  private sendTerminalInput(): void {
    if (!this.terminalInput) return;
    this.workspace.sendTerminalText(this.terminalInput);
    this.workspace.sendTerminalKey('enter');
    this.terminalInput = '';
  }

  private measureTerminal(area: Area): void {
    const width = Number(area.width), height = Number(area.height);
    if (width > 0 && height > 0) this.workspace.resizeTerminal(Math.floor(width / 8), Math.floor(height / 18));
  }

  private async pickUpload(): Promise<void> {
    this.transferError = '';
    this.pickerBusy = true;
    try {
      const options = new picker.DocumentSelectOptions();
      options.maxSelectNumber = 1;
      const viewPicker = new picker.DocumentViewPicker(this.uiContext());
      const uris = await viewPicker.select(options);
      if (uris.length === 0) return;
      const selected = this.readPickedFile(uris[0]);
      await this.workspace.uploadFile(selected.name, selected.data);
    } catch (error) { this.transferError = this.errorText(error as Error); }
    finally { this.pickerBusy = false; }
  }

  private readPickedFile(uri: string): PickedFile {
    const info = fileIo.statSync(uri);
    if (info.size <= 0 || info.size > TRANSFER_LIMIT) throw new Error('transfer_limit');
    const file = fileIo.openSync(uri, fileIo.OpenMode.READ_ONLY);
    let bytes: ArrayBuffer = new ArrayBuffer(0);
    try {
      const buffer = new ArrayBuffer(info.size);
      const read = fileIo.readSync(file.fd, buffer);
      if (read !== info.size) throw new Error('file_read_failed');
      bytes = buffer;
    } finally { fileIo.closeSync(file.fd); }
    const result = new PickedFile();
    const leaf = uri.substring(uri.lastIndexOf('/') + 1);
    try { result.name = decodeURIComponent(leaf); } catch (_) { result.name = leaf; }
    if (!result.name) result.name = 'upload';
    result.data = bytes;
    return result;
  }

  private async saveDownload(): Promise<void> {
    if (!this.fileOpen || !this.workspace.selectedFile) return;
    const revision = this.workspace.currentRevision(), target = this.workspace.currentTarget(), path = this.workspace.selectedFile;
    this.transferError = '';
    this.pickerBusy = true;
    try {
      const file = await this.workspace.downloadFile();
      if (!this.fileOpen || !this.workspace.isCurrent() || revision !== this.workspace.currentRevision() ||
        target !== this.workspace.currentTarget() || path !== this.workspace.selectedFile) throw new Error('request_updated');
      const options = new picker.DocumentSaveOptions();
      options.newFileNames = [file.name];
      const viewPicker = new picker.DocumentViewPicker(this.uiContext());
      const uris = await viewPicker.save(options);
      if (uris.length === 0) return;
      if (!this.fileOpen || !this.workspace.isCurrent() || revision !== this.workspace.currentRevision() ||
        target !== this.workspace.currentTarget() || path !== this.workspace.selectedFile) throw new Error('request_updated');
      const output = fileIo.openSync(uris[0], fileIo.OpenMode.READ_WRITE);
      try {
        const written = fileIo.writeSync(output.fd, file.data);
        if (written !== file.data.byteLength) throw new Error('file_write_failed');
      } finally { fileIo.closeSync(output.fd); }
    } catch (error) { this.transferError = this.errorText(error as Error); }
    finally { this.pickerBusy = false; }
  }

  private sizeText(size: number): string {
    if (size < 1024) return String(size) + ' B';
    if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB';
    if (size < 1024 * 1024 * 1024) return (size / (1024 * 1024)).toFixed(1) + ' MB';
    return (size / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
  }

  build() {
    Tabs({ index: this.workspace.tab }) {
      TabContent() { this.fileView() }.tabBar(tr('files'))
      TabContent() { this.gitView() }.tabBar(tr('git'))
      TabContent() { this.terminalView() }.tabBar(tr('terminal'))
      TabContent() { this.terminalScreenView() }.tabBar(tr('session_tools_terminal_screen'))
      TabContent() { this.previewView() }.tabBar(tr('preview'))
    }.barMode(BarMode.Scrollable).barHeight('auto').onChange((index: number) => {
      this.workspace.selectTab(index);
      this.pathDraft = this.workspace.currentPath;
    }).width('100%').height('100%')
  }
}
