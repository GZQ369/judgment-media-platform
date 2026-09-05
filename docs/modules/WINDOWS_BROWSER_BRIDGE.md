# Windows Browser Draft Runtime

`tools/windows-browser-bridge` is the Windows compatibility layer for the
video-publisher Skill. It implements the behavior contract used by
`run-platform.mjs`; it does not copy Ego Lite private code and it does not
provide a final-publish primitive.

## Position in the system

```text
Judgment / Offering / Manual Content
              -> ContentAtom
              -> Content Distribution Pipeline
              -> Publisher Gateway
              -> Windows Browser Draft Runtime
              -> creator pages
```

The runtime is an adapter only. Research, Judgment, workflow, rendering,
exporting and the existing Publisher business behavior remain outside this
directory.

## Install

Requirements:

- Windows 10/11
- Node.js 20+
- Go 1.23+ to build the launcher
- `npm install` in this directory, which installs only `playwright-core`
- A locally installed Chrome or Edge

Run `install.ps1` from PowerShell. It builds `bin\ego-browser.exe` and prints
the exact environment variable command. It does not modify PATH or any system
configuration.

```powershell
Set-Location tools\windows-browser-bridge
npm install
.\install.ps1
$env:VIDEO_PUBLISHER_V2_EGO_COMMAND = "D:\agent\vertical-intel-video-agent\tools\windows-browser-bridge\bin\ego-browser.exe"
```

The `.exe` is the supported entry for Node's `spawn(command, args,
{shell:false})`. `ego-browser.cmd` is only a convenience wrapper for a human
PowerShell or cmd session.

## Runtime layout

The daemon uses one persistent Playwright BrowserContext and a dedicated
profile. It never opens the user's ordinary Chrome or Edge profile.

```text
%LOCALAPPDATA%\JudgmentMedia\browser-bridge\
  profile\
  state\task-spaces.json
  state\task-spaces.backup.json
  auth.token
  logs\
  runs\
```

The daemon listens only on a Windows named pipe. The pipe is bearer-token
authenticated in addition to Windows local-user isolation. `auth.token` is
outside the repository and is never placed in task-space state or logs.

Chrome is checked before Edge. Set `WINDOWS_BROWSER_BRIDGE_EXECUTABLE` to use a
specific installed executable. Set `WINDOWS_BROWSER_BRIDGE_MEDIA_ROOT` to
restrict upload files to one absolute directory. The default accepts regular
video and image files but rejects profile, state, credentials, secrets,
cookies, tokens and auth paths.

## Commands

```powershell
& .\bin\ego-browser.exe status
& .\bin\ego-browser.exe stop
```

The first `nodejs` invocation starts a detached hidden daemon when it is not
already healthy. The daemon and browser remain alive after the ESM task exits.

The command reads the complete ESM program from stdin, writes a temporary
`.mjs`, and starts Node with `--import src\bootstrap.mjs`. Static imports and
top-level `await` therefore work as they do in the maintained Skill. `cliLog`
is the only normal stdout helper and writes the supplied value unchanged;
runtime errors go to stderr and produce a non-zero exit code.

## Compatibility API

The bootstrap injects these globals:

```text
useOrCreateTaskSpace(ref)  claimTaskSpace(ref)
listTabs()                switchTab(targetId)
openOrReuseTab(url, opts) pageInfo()
js(source)                cdp(method, params)
click(selector|[x,y], o)  hover(selector|[x,y], o)
pressKey(key)             uploadFile(selector, absolutePath)
gotoAndWait(url, opts)    wait(seconds)
cliLog(value)
```

Task-space IDs are numeric and names are stable. Numeric lookup of a missing
space fails with an error containing `task space not found`. Every session
leases its selected space; another session using that space fails immediately.
Page operations are serialized per page and input/focus operations share a
global input queue. Uploads can proceed on different pages after path
validation.

## Safety boundary

Every context and page receives a capture-phase guard for the exact labels
`发布`, `发布笔记`, `发表` and `立即投稿`. Selector clicks, coordinate clicks,
Enter presses and the page's own trusted click event are checked. The runtime
does not expose a publish command, does not persist publish authorization and
never turns a draft preparation result into a published result.

`js()` and `Runtime.evaluate` execute trusted local Skill code. They are not a
malicious-code sandbox, so the `ego-browser` command must remain a local,
trusted executable and must not be exposed as a remote service.

The permitted CDP methods are only:

```text
Runtime.evaluate
DOM.setFileInputFiles
Page.bringToFront
Page.setWebLifecycleState
Emulation.setFocusEmulationEnabled
Input.insertText
```

## Tests and smoke

Most tests use fake drivers and do not start a browser:

```powershell
npm test
node --check src\cli.mjs
node --check src\daemon.mjs
node --check src\browser.mjs
```

The optional smoke uses a temporary dedicated profile and a `data:` page. It
does not visit any creator platform or publish anything:

```powershell
npm run smoke
```

The Windows launcher integration test runs only after `install.ps1` has built
`bin\ego-browser.exe`; otherwise it is reported as skipped. It invokes
`spawn(absExe, ["nodejs"], {shell:false})` and sends an ESM program through
stdin.
