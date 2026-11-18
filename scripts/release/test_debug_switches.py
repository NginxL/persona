#!/usr/bin/env python3
"""debug_switches.txt stays a sorted list of unique switch names and keeps every name release.sh's regex had, and
debug_switches.py finds a switch where release.sh's `grep -qxE` matched, a whole line of `strings` output, anywhere in
all of it (#10616). The runtime CLI rule it also runs is tested in test_release_debug_guard.py."""
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import debug_switches as ds

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(ds.__file__).resolve()

# Every name on release.sh's `grep -qxE '(…)'` line when the list moved to debug_switches.txt (#10616). A name leaves
# the list only on purpose (a switch whose name now ships in Release code): take it out here in the same change. A
# union merge can bring a removed name back, which this does not notice: check union merges that delete names.
SNAPSHOT = """
addMail dropUsers fleetDetails fleetIsland focusComposer focusTerminalInput holdChromeOpen inboxDrawer inboxJumpWait
jumpEnd larkFocus larkMoments larkPicker larkProfile larkTop loadAll newAssistant openAbout openAgent openAgentsFresh
openAllWork openAppearance openAssistant openAssistantManager openAssistantPage openAssistantSidebar openBrowserLogins
openCalendarDate openCalendarEvent openChangeAnswer openChannel openChannels openChatInfo openChatInfoFrom
openChatInfoPage openChatsFresh openClaudeSettings openCloudflareAccess openCodexSettings openConnectDevice
openConnectionDetails openConnectorBot openConnectorDevice openConnectorFeishu openConnectorGitHub openConnectorMail
openConnectorMessages openConnectorPlugin openConnectorTelegram openConnectors openDayCatchUp openDayRecap
openDesktopSheet openDevices openDictationControl openDraftsFresh openEditor openEngineStep openExtension
openExtensionManager openExtensionMarket openExtensionMarkets openExtensionPage openExtensions openFeishuDoc
openFeishuDocHeading openFeishuWikiNode openFeishuWikiSpace openFileLine openFilePath openFirstImage openFromDoneNotice
openHub openHubAddresses openHubUpdate openHubs openIPhoneSetup openImagesFresh openInboxAsk openInboxFirst
openInboxItem openInboxRule openInboxSources openIslandDiagnostics openIslandSettings openKnowledgeSelection
openLauncherDetails openLauncherEditor openLauncherPage openLauncherPicker openLinkURL openLiveScreenSettings
openLocalProxy openMacExtensionPage openMacListFocus openMacPluginPage openMacSection openMacTodo openMachineClipboard
openMachineDetail openMachineFiles openMachinePage openMachines openMailLabel openMailThread openManagerPage
openManagers openMarketExtension openMarketPlugin openMeeting openMeetingAudio openMeetingSettings openMeetingTapes
openMentionMenu openMessagesChat openMessagesChats openMinutes openModelConfiguration openModelLauncher
openModelLaunchers openModelPurpose openMomentsTab openMonitor openMonitorRange openMonitorScrub openNewAgentAttachments
openNotificationSettings openOKR openOverSpace openOwnerCall openOwnerCallSeconds openPairLink openPairLinkTimes
openPairSSH openPairSheet openPane openPeople openPeopleQuery openPersona openPlugin openPluginMarket openPluginMarkets
openPluginPage openPlugins openPreviewServers openProviderComputers openProviderWizard openQuietSpaces openRecapsFresh
openRecording openReportRecord openRescheduleTitled openSSHTunnel openScreenDemo openScreenFresh openSearch
openSearchFeed openSearchFilter openSearchFirst openSearchTab openSendDelay openSendMessage openSentAsYou openSession
openShellPage openShortcutsPanel openSkills openSlashMenu openSourcesGraph openSpaceFromChat openSplitFresh openStorage
openStorageClear openSubscriptions openTask openTaskScope openTerminalPane openTerminalSettings openTerminalTips
openTerminalsPage openThreadAt openTodoDetail openTodoTitled openTokenUsage openTranslationSettings openTriageSettings
openUploadDemo openUrgentCallSettings openViewedImage openVoice openWelcome openWorkbench openWorkoutReports pairURL
pairURL2 pairURL3 perfCounts scrollTrace scrollTracePath searchTracePath seedCloudflareAccess seedHubAddresses
seedLocalProxy seedSSHKey seedSSHTunnel showAddComputer showAgentInfo showAgentSortShadow showAnswerPartLines
showAnswerWhole showAsUnread showAssistantSwitcher showAttachCamera showAutoScroll showAvatarCrop showAvatarCropName
showAvatarDelete showBlankLanding showBrowserLoginSample showButtonContrast showCalendarNow showCallFallback
showCallSample showCallSampleAgent showCallSampleCaption showCallSampleLook showCallSampleName showCallSampleReason
showCameraRows showChatDump showCleanUpConfirm showClearConfirm showClearedMain showClipboardFetch showClipboardHint
showClockAt showCloseAgent showCloseoutSample showCompactChrome showComposerPhoto showConnection showConnectionCycle
showContexts showContinuityShadow showDaySample showDeleteConfirm showDeleteSession showDesktopSample showDetailBottom
showDictationHold showDictationSample showDiscardConfirm showDocCommentDraft showDocCommentReply showDoneTodos
showDrawerAutoDrag showDrawerProbe showEdgeScrub showErrorDetails showExport showFeishuDocSheet showFeishuDocTableEnd
showFeishuWikiUpdates showFileAutoScroll showFileAutoScrollSpeed showFilePerf showFilesDrop showFleetCount
showFleetSample showFloatSpeaker showFoldProbe showFolderUpload showFrameProbe showFreezeProbe showFromSeq
showFullOutput showGitReload showHandOffNewAgent showHealthTrends showHerdrStart showHomeState showHomeWidgetSample
showHomeWidgets showHubAddressDraft showInboxAction showInboxDone showInboxDoneFolded showInboxDue showInboxJump
showInboxMerge showInboxMutes showInboxPickGroups showInboxPicked showInboxPickedDone showInboxRules showInboxSection
showInboxSuggestionEdit showInboxSuggestionStale showInboxTodo showInfo showJumpPerf showKeyMods showKnowledgeDay
showKnowledgeDelete showKnowledgeEntry showKnowledgeFacts showKnowledgeKind showKnowledgeLower showKnowledgeMap
showKnowledgeMapPoint showKnowledgePoint showKnowledgeSearch showKnowledgeUnlink showLark showLarkAttachmentDraft
showLarkQuoteDraft showLarkSendFresh showLarkThreadAttachmentDraft showLauncherCheck showLauncherHelp showLiftLines
showListOffset showLookBlink showLoopDraft showMacDropCover showMacHover showMacKeyScript showMailAskClaude
showMailAttachment showMailBottom showManagerCode showMeetingReminder showMeetingReplay showMeetingReplayEnd
showMeetingReplaySpeed showMeetingSample showMenu showMinutesPlaying showMinutesSearch showMinutesTab
showModelConfigSheet showModelSearch showNewAgentPrompt showNewChat showNewMain showOldest showPad showPagePerf
showPagePerfRepeat showPagePerfSeconds showPagePerfSpeed showPairAccess showPerfChanges showPerfChurn showPerfDelay
showPerfGlide showPerfOut showPerfPoint showPerfQuit showPerfRounds showPerfRun showPerfSeconds showPerfSections
showPerfSpeed showPersonaState showPiPSource showPlainBubbleOpen showPlanSheet showPopFrames showPreviewPicking
showPromptMenu showPushAsk showPushHealth showPushKey showQuestionStepPause showRecapDrawer showRecordingEnd
showRecordingSample showRemovalWarnings showReportDelete showReportUpload showReportUploadMB showRewind showRunAgentHere
showRunHereLauncher showRunsOpen showSSHViaTunnel showSameIcon showSampleCodexApproval showSampleCodexQuestion
showSampleComments showSampleHTMLCard showSamplePermission showSampleQuestion showSampleQuotes showSampleScreen
showScreenFallbackDemo showScreenMobileData showScreenRoutes showScreenStatsDemo showScreenUDP showScreenVideoDemo
showScreenX264Demo showScreenX264OffDemo showScrollState showScrollToOldest showSearchTrace showShareCard
showShellPagerAt showSideSessions showSpaceTabs showStartGrantSample showSubagentDetails showSupervisionOpen
showSwiftUIJumps showSystemCallService showTaskBoard showTaskDetail showTerminalUpload showTerminalsUnlinked
showTestNotification showTestNotificationIn showToasts showTodoHandoff showTokenPick showToolDetails showUnfoldLab
showUnlockWithoutAuth showUpcomingSample showUrgentCallSample showUrgentCallSampleTitle showViewerChromeHidden
showViewerDecodeDelay showViewerDecodeDuringPan showViewerInfo showViewerPagingProbe showVoiceOrbs showVoiceSample
showWatchNote showWholeBubbles showWorkBottom simulateOffline simulateTap swapTreeAt swapTreeAway swapTreeWhole
terminalPages unfoldTrace wizardKey wizardPreset
""".split()


def run(*args, cwd=None):
    return subprocess.run([sys.executable, str(SCRIPT), *args], capture_output=True, text=True, cwd=cwd, timeout=60)


class DebugSwitchListTest(unittest.TestCase):
    def test_the_list_is_sorted_unique_and_well_formed(self):
        self.assertEqual(ds.problems(ds.LIST.read_text(encoding="utf-8")), [],
                         "fix debug_switches.txt; python3 scripts/release/debug_switches.py sort puts it in order (a "
                         "union merge can leave two new names out of order, or the same name twice)")

    def test_no_name_of_the_old_regex_is_dropped(self):
        self.assertEqual(len(set(SNAPSHOT)), len(SNAPSHOT))
        self.assertGreater(len(SNAPSHOT), 400)
        missing = sorted(set(SNAPSHOT) - set(ds.names()))
        self.assertEqual(missing, [], "these DEBUG switches left debug_switches.txt: put them back")

    def test_the_header_explains_the_file(self):
        header, listed = ds.split(ds.LIST.read_text(encoding="utf-8"))
        self.assertTrue(header and all(line.startswith("#") for line in header))
        self.assertTrue(listed)

    def test_problems_names_what_is_wrong(self):
        self.assertEqual(ds.problems("# header\nopenA\nshowB\n"), [])
        self.assertEqual(ds.problems("openB\nopenA\nopenA\n"), ["openA comes before openB", "openA is listed twice"])
        # Byte order, as LC_ALL=C sort: upper case before lower case.
        self.assertEqual(ds.problems("openOKR\nopenOk\n"), [])
        self.assertEqual(ds.problems("openA\n<<<<<<< HEAD\n"), ["line 2: '<<<<<<< HEAD' is not a switch name "
                                                                "(letters, digits and _)"])
        self.assertEqual(ds.problems("openA\nshowB "), ["line 2: 'showB ' is not a switch name "
                                                        "(letters, digits and _)"])
        self.assertEqual(ds.problems("openA\nshowB"), ["the last line has no newline"])

    def test_comments_and_blank_lines_belong_to_the_header(self):
        # Above the first name they are the header, kept as they are; below it they are refused, since sort could
        # not keep them next to the name they were about.
        self.assertEqual(ds.split("# one\n\n# two\nopenA\n"), (["# one", "", "# two"], ["openA"]))
        below = [("# h\nopenA\n# why showB stays\nshowB\n", 3, "a comment"),
                 ("openA\n  # indented\nshowB\n", 2, "a comment"),
                 ("# h\nopenA\n\nshowB\n", 3, "a blank line"),
                 ("# h\nopenA\nshowB\n\n", 4, "a blank line")]
        for text, line, what in below:
            with self.subTest(text=text):
                expected = (f"line {line}: {what} below the first name (comments go in the header; "
                            "no blank lines between names)")
                self.assertEqual(ds.problems(text), [expected])
                with tempfile.TemporaryDirectory() as tmp:
                    path, strings = Path(tmp, "list.txt"), Path(tmp, "strings.txt")
                    path.write_text(text, encoding="utf-8")
                    strings.write_text("harmlessName\n", encoding="utf-8")
                    result = run("--list", str(path), "sort")
                    self.assertEqual(result.returncode, 2)
                    self.assertIn(expected, result.stderr)
                    self.assertEqual(path.read_text(encoding="utf-8"), text, "sort must not drop the line")
                    self.assertEqual(run("--list", str(path), "check", "--strings", str(strings)).returncode, 2)

    def test_sort_leaves_a_list_with_no_problems_as_it_is(self):
        samples = ("# one\n\n# two\nopenA\nopenOKR\nopenOk\nshowB\n", "openA\n", "# only a header\n",
                   "# h\nshowB\nopenA\n", "openA\nopenA\n", "openA\nshowB", ds.LIST.read_text(encoding="utf-8"))
        for text in samples:
            with self.subTest(text=text[:40]):
                self.assertEqual(ds.problems(text) == [], ds.sort_text(text) == text)

    def test_sort_keeps_the_header_and_drops_duplicates(self):
        text = "# one\n# two\nshowB\nopenA\nshowB\nopenC\n"
        self.assertEqual(ds.sort_text(text), "# one\n# two\nopenA\nopenC\nshowB\n")
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp, "list.txt")
            path.write_text(text, encoding="utf-8")
            self.assertEqual(run("--list", str(path), "sort").returncode, 0)
            self.assertEqual(path.read_text(encoding="utf-8"), "# one\n# two\nopenA\nopenC\nshowB\n")
            self.assertEqual(ds.problems(path.read_text(encoding="utf-8")), [])


class DebugSwitchCheckTest(unittest.TestCase):
    def test_found_matches_whole_lines_only(self):
        output = b"openSession\nharmlessName\nxopenSession\nopenSession2\n-showInfo\nshowInfo \n"
        self.assertEqual(ds.found(output, ["openSession", "showInfo"]), ["openSession"])
        self.assertEqual(ds.found(b"harmlessName\n", ["openSession"]), [])

    def test_a_strings_file_with_a_switch_fails_and_one_without_passes(self):
        switch = "openSession"
        self.assertIn(switch, ds.names())
        with tempfile.TemporaryDirectory() as tmp:
            dirty, clean = Path(tmp, "dirty.txt"), Path(tmp, "clean.txt")
            dirty.write_text(f"harmlessName\n{switch}\n", encoding="utf-8")
            clean.write_text("harmlessName\nopenSessionHarmless\n", encoding="utf-8")
            result = run("check", "--strings", str(dirty))
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stderr.splitlines(), [ds.MESSAGE, f"  {switch}"])
            result = run("check", "--strings", str(clean))
            self.assertEqual((result.returncode, result.stderr), (0, ""))

    @unittest.skipUnless(shutil.which("strings"), "needs strings")
    def test_a_binary_is_read_through_strings(self):
        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp, "Persona")
            binary.write_bytes(b"\x00\x01\x02harmlessName\x00\x03\x04showNewChat\x00\x05")
            result = run("check", str(binary))
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual(result.stderr.splitlines(), [ds.MESSAGE, "  showNewChat"])
            binary.write_bytes(b"\x00\x01\x02harmlessName\x00\x05")
            self.assertEqual(run("check", str(binary)).returncode, 0)

    def test_an_empty_or_broken_list_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            strings = Path(tmp, "strings.txt")
            strings.write_text("harmlessName\n", encoding="utf-8")
            empty, broken = Path(tmp, "empty.txt"), Path(tmp, "broken.txt")
            empty.write_text("# only a header\n", encoding="utf-8")
            broken.write_text("openA\n=======\nopenB\n", encoding="utf-8")
            for path in (empty, broken, Path(tmp, "missing.txt")):
                with self.subTest(path=path.name):
                    self.assertEqual(run("--list", str(path), "check", "--strings", str(strings)).returncode, 2)
            self.assertEqual(run("check", str(Path(tmp, "no-such-binary"))).returncode, 2)

    def test_release_sh_runs_the_check(self):
        text = (ROOT / "scripts/release/release.sh").read_text(encoding="utf-8")
        self.assertIn('python3 scripts/release/debug_switches.py check "$APP/Persona"\n', text)
        self.assertNotIn("grep -qxE '(", text, "the switches live in scripts/release/debug_switches.txt now")

    def test_git_merges_the_list_by_union(self):
        lines = (ROOT / ".gitattributes").read_text(encoding="utf-8").splitlines()
        self.assertIn("scripts/release/debug_switches.txt merge=union", lines)


if __name__ == "__main__":
    unittest.main()
