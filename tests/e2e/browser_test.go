//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

func TestProductionBinaryInRealBrowser(t *testing.T) {
	binary := productionBinary(t)
	chrome := chromeExecutable(t)
	listenerAddress := freeAddress(t)
	baseURL := "http://" + listenerAddress
	dataDir := filepath.Join(t.TempDir(), "browser-data")
	sshServer := startSFTPServer(t, filepath.Join(t.TempDir(), "remote"))
	defer sshServer.Close()
	process := startProductionBinary(t, binary, dataDir, listenerAddress)
	defer func() { process.Stop(t) }()
	waitForHealth(t, baseURL)

	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome), chromedp.Headless, chromedp.NoSandbox,
	)...)
	defer cancelAllocator()
	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 4*time.Minute)
	defer cancelTimeout()

	runBrowser(t, ctx,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(baseURL),
		chromedp.WaitVisible(`input[autocomplete="username"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[autocomplete="username"]`, "admin", chromedp.ByQuery),
		chromedp.SendKeys(`(//input[@autocomplete="new-password"])[1]`, "correct horse battery staple", chromedp.BySearch),
		chromedp.SendKeys(`(//input[@autocomplete="new-password"])[2]`, "correct horse battery staple", chromedp.BySearch),
		clickButton("创建管理员"),
		waitHeading("仪表盘"),
	)
	repositoryID, restoreTarget := prepareBrowserDirectorySnapshot(t, baseURL)

	// Every top-level resource view is rendered by the final embedded React bundle.
	for _, page := range []string{"兼容性中心", "远程主机", "Agent 节点", "数据库实例", "备份仓库", "备份任务", "快照与恢复", "运行记录", "告警历史", "投递记录", "审计日志", "通知配置", "Agent 服务", "安全设置", "配置备份与恢复", "数据生命周期"} {
		t.Logf("open browser page %s", page)
		runBrowser(t, ctx, clickPage(page))
		if navigationGroup(page) != "" {
			runBrowser(t, ctx, waitSelectedTab(page))
			continue
		}
		heading := page
		if page == "快照与恢复" {
			heading = "从快照恢复"
		}
		runBrowser(t, ctx, waitHeading(heading))
	}
	var pageText string
	runBrowser(t, ctx, chromedp.Text("body", &pageText, chromedp.ByQuery))
	if !strings.Contains(pageText, "当前应用版本") {
		t.Fatalf("sidebar is missing the current application version: %q", pageText)
	}

	// Retired top-level modules redirect to the task workflow.
	for _, legacyPath := range []string{"/admin/protection", "/admin/plans"} {
		runBrowser(t, ctx, chromedp.Navigate(baseURL+legacyPath), waitHeading("备份任务"))
	}

	// Exercise the long-term history, trend, background-capacity, and redacted
	// diagnostic paths through the final embedded bundle.
	t.Log("exercise durable history, capacity health, and diagnostic downloads")
	downloadDirectory := t.TempDir()
	runBrowser(t, ctx, browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(downloadDirectory))
	runBrowser(t, ctx,
		clickNavigation("仪表盘"), waitHeading("仪表盘"),
		chromedp.WaitVisible(`//*[contains(normalize-space(.),"成功率分母只包含完整成功、部分成功和失败")]`, chromedp.BySearch),
		clickPage("运行记录"), waitSelectedTab("运行记录"),
		setSelectByLabel("状态筛选", "success"), clickButton("应用筛选"),
		chromedp.Poll(`new URLSearchParams(location.search).get('status') === 'success'`, nil),
		chromedp.WaitVisible(`//tbody/tr[.//*[@aria-label="状态：成功"]]`, chromedp.BySearch),
		chromedp.Evaluate(`history.back()`, nil),
		chromedp.Poll(`!new URLSearchParams(location.search).has('status')`, nil),
		chromedp.Poll(`(() => { const label = [...document.querySelectorAll('label')].find((item) => item.textContent.includes('状态筛选')); return label?.querySelector('select')?.value === ''; })()`, nil),
		chromedp.Evaluate(`history.forward()`, nil),
		chromedp.Poll(`new URLSearchParams(location.search).get('status') === 'success'`, nil),
		chromedp.Poll(`(() => { const label = [...document.querySelectorAll('label')].find((item) => item.textContent.includes('状态筛选')); return label?.querySelector('select')?.value === 'success'; })()`, nil),
		chromedp.Click(`//a[normalize-space(.)="导出当前筛选"]`, chromedp.BySearch),
	)
	activityCSV := waitForBrowserDownload(t, downloadDirectory, "shadoc-activity.csv", 10*time.Second)
	activityRows, err := csv.NewReader(bytes.NewReader(activityCSV)).ReadAll()
	if err != nil || len(activityRows) < 2 {
		t.Fatalf("browser activity CSV rows=%d err=%v", len(activityRows), err)
	}
	for _, row := range activityRows[1:] {
		if len(row) < 5 || row[4] != "success" {
			t.Fatalf("browser activity export ignored the applied status filter: %v", row)
		}
	}
	runBrowser(t, ctx,
		clickPage("兼容性中心"), waitSelectedTab("兼容性中心"),
		clickButton("下载脱敏诊断包"),
		chromedp.WaitVisible(`//*[normalize-space(.)="脱敏诊断包已下载"]`, chromedp.BySearch),
	)
	diagnosticJSON := waitForBrowserDownload(t, downloadDirectory, "shadoc-diagnostics.json", 10*time.Second)
	var diagnostic struct {
		FormatVersion int `json:"formatVersion"`
	}
	if err := json.Unmarshal(diagnosticJSON, &diagnostic); err != nil || diagnostic.FormatVersion != 1 {
		t.Fatalf("browser diagnostic download format=%d err=%v", diagnostic.FormatVersion, err)
	}
	waitForBackgroundCapacity(t, baseURL, repositoryID, 45*time.Second)
	runBrowser(t, ctx,
		clickPage("备份仓库"), waitHeading("备份仓库"),
		chromedp.WaitVisible(`//tr[td[normalize-space(.)="浏览器恢复仓库"]]//*[contains(normalize-space(.),"可用 / 共")]`, chromedp.BySearch),
		chromedp.WaitVisible(`//tr[td[normalize-space(.)="浏览器恢复仓库"]]//button[@aria-label="刷新存储容量"]`, chromedp.BySearch),
	)
	t.Log("durable history, capacity health, and diagnostic downloads complete")

	// Complete a real directory restore through the production browser UI.
	t.Log("exercise complete browser restore")
	runBrowser(t, ctx, clickNavigation("快照与恢复"))
	runBrowser(t, ctx,
		waitNavigation("快照与恢复"),
		waitHeading("从快照恢复"),
	)
	t.Log("browser restore page visible")
	runBrowser(t, ctx,
		chromedp.WaitReady(fmt.Sprintf(`//label[contains(normalize-space(.),"仓库")]//option[@value=%q]`, repositoryID), chromedp.BySearch),
		setSelectByLabel("仓库", repositoryID),
		chromedp.WaitReady(`//label[contains(normalize-space(.),"目录快照")]//option[@value!=""]`, chromedp.BySearch),
	)
	t.Log("browser restore snapshots loaded")
	var snapshotOptionText string
	runBrowser(t, ctx, chromedp.Evaluate(`document.evaluate('(//label[contains(normalize-space(.),"目录快照")]//option[@value!=""])[1]', document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue.textContent`, &snapshotOptionText))
	if !strings.Contains(snapshotOptionText, "… · ") || strings.Contains(snapshotOptionText, "T") {
		t.Fatalf("browser snapshot option is not shortened: %q", snapshotOptionText)
	}
	runBrowser(t, ctx, selectFirstNonEmptyByLabel("目录快照"), clickButton("浏览并选择快照内容"), waitHeading("浏览快照内容"))
	t.Log("browser snapshot content page visible")
	runBrowser(t, ctx,
		chromedp.Click(`//button[contains(normalize-space(.),"返回恢复设置")]`, chromedp.BySearch),
		waitHeading("从快照恢复"),
		chromedp.SendKeys(`//label[contains(normalize-space(.),"新目标绝对路径")]//input`, restoreTarget, chromedp.BySearch),
		clickButton("执行只读预检"),
		chromedp.WaitVisible(`//*[normalize-space(.)="预检通过"]`, chromedp.BySearch),
	)
	t.Log("browser restore preflight complete")
	runBrowser(t, ctx,
		chromedp.SendKeys(`//label[contains(normalize-space(.),"当前管理员密码")]//input`, "correct horse battery staple", chromedp.BySearch),
		clickButton("确认并开始目录恢复"),
		chromedp.WaitVisible(`//*[normalize-space(.)="恢复完成"]`, chromedp.BySearch),
	)
	content, err := os.ReadFile(filepath.Join(restoreTarget, "album", "payload.txt"))
	if err != nil || string(content) != "browser restore payload" {
		t.Fatalf("browser restore result=%q err=%v", content, err)
	}
	t.Log("complete browser restore finished")

	// Fetch and confirm a real SSH host key, then create, edit, and delete the host through the UI.
	t.Log("exercise remote host UI")
	runBrowser(t, ctx,
		clickPage("远程主机"), waitHeading("远程主机"), clickButton("新建远程主机"),
		chromedp.WaitVisible(`form[role="dialog"]`, chromedp.ByQuery),
		chromedp.Click(`//label[normalize-space(.)="导入现有私钥"]//input`, chromedp.BySearch),
		chromedp.SendKeys(`input[name="name"]`, "E2E NAS", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="host"]`, "127.0.0.1", chromedp.ByQuery),
		chromedp.SetValue(`input[name="port"]`, fmt.Sprint(sshServer.Port), chromedp.ByQuery),
		chromedp.SendKeys(`input[name="username"]`, "backup", chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="privateKey"]`, string(sshServer.ClientPrivateKey), chromedp.ByQuery),
		clickButton("获取并核对主机密钥"),
		chromedp.WaitVisible(`.host-key-confirmation`, chromedp.ByQuery),
	)
	t.Log("remote host key fetched")
	var knownHosts string
	runBrowser(t, ctx, chromedp.Value(`textarea[name="hostFingerprint"]`, &knownHosts, chromedp.ByQuery))
	if knownHosts != sshServer.KnownHostsLine {
		t.Fatalf("browser host-key confirmation=%q want=%q", knownHosts, sshServer.KnownHostsLine)
	}
	runBrowser(t, ctx,
		clickButton("保存"),
		chromedp.WaitVisible(`//tr[td[normalize-space(.)="E2E NAS"]]`, chromedp.BySearch),
		chromedp.Click(`//tr[td[normalize-space(.)="E2E NAS"]]//button[starts-with(@aria-label,"复制 ID ")]`, chromedp.BySearch),
		chromedp.WaitVisible(`//*[starts-with(normalize-space(.),"已复制 ID：")]`, chromedp.BySearch),
		chromedp.Click(`//tr[td[normalize-space(.)="E2E NAS"]]//button[normalize-space(.)="编辑"]`, chromedp.BySearch),
		chromedp.WaitVisible(`form[role="dialog"]`, chromedp.ByQuery),
		chromedp.SetValue(`input[name="name"]`, "E2E NAS 已编辑", chromedp.ByQuery),
		clickButton("保存"),
		chromedp.WaitVisible(`//tr[td[normalize-space(.)="E2E NAS 已编辑"]]`, chromedp.BySearch),
	)
	t.Log("remote host created and edited")
	runBrowser(t, ctx,
		chromedp.Click(`//tr[td[normalize-space(.)="E2E NAS 已编辑"]]//button[normalize-space(.)="删除"]`, chromedp.BySearch),
		chromedp.WaitVisible(`//*[@role="dialog" and .//*[normalize-space(.)="确认删除远程主机"]]`, chromedp.BySearch),
	)
	t.Log("remote host delete preview visible")
	runBrowser(t, ctx,
		clickButton("确认删除"),
		chromedp.WaitNotPresent(`//tr[td[normalize-space(.)="E2E NAS 已编辑"]]`, chromedp.BySearch),
	)
	t.Log("remote host UI complete")

	// A generated key stays in the application; the browser only receives the
	// public key and the instructions for authorizing it on the remote host.
	runBrowserDiagnostic(t, ctx, process, "open generated-key remote host dialog",
		clickButton("新建远程主机"),
		chromedp.WaitVisible(`form[role="dialog"]`, chromedp.ByQuery),
	)
	t.Log("generated-key remote host dialog open")
	runBrowserDiagnostic(t, ctx, process, "fetch generated-key remote host fingerprint",
		chromedp.SendKeys(`input[name="name"]`, "Generated E2E NAS", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="host"]`, "127.0.0.1", chromedp.ByQuery),
		chromedp.SetValue(`input[name="port"]`, fmt.Sprint(sshServer.Port), chromedp.ByQuery),
		chromedp.SendKeys(`input[name="username"]`, "backup", chromedp.ByQuery),
		clickButton("获取并核对主机密钥"),
		chromedp.WaitVisible(`.host-key-confirmation`, chromedp.ByQuery),
	)
	t.Log("generated host key fetched")
	runBrowserDiagnostic(t, ctx, process, "save generated-key remote host", clickButton("保存"))
	var generatedKeyOutcome string
	if err := chromedp.Run(ctx, chromedp.Poll(`(() => {
		if (document.querySelector('#generated-ssh-public-key-title')) return 'authorization';
		const error = document.querySelector('form[role="dialog"] .form-error');
		return error ? 'error:' + error.textContent : '';
	})()`, &generatedKeyOutcome, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		var body string
		_ = chromedp.Run(ctx, chromedp.Text("body", &body, chromedp.ByQuery))
		logBody, _ := os.ReadFile(process.logFile.Name())
		t.Fatalf("generated SSH key flow did not resolve: %v\npage=%s\nservice log=%s", err, body, logBody)
	}
	if generatedKeyOutcome != "authorization" {
		t.Fatalf("generated SSH key flow result=%q", generatedKeyOutcome)
	}
	var authorizationCommand string
	runBrowser(t, ctx, chromedp.Value(`textarea[aria-label="服务器授权命令"]`, &authorizationCommand, chromedp.ByQuery))
	if !strings.Contains(authorizationCommand, "~/.ssh/authorized_keys") {
		t.Fatalf("generated key instructions omit authorized_keys path: %q", authorizationCommand)
	}
	runBrowser(t, ctx, clickButton("完成"))

	// Exercise task-local schedule controls with the fixture task available.
	t.Log("exercise task scheduling and lifecycle UI")
	runBrowser(t, ctx,
		clickPage("备份任务"), waitHeading("备份任务"),
		chromedp.Click(`//tr[td[normalize-space(.)="浏览器恢复任务"]]//button[normalize-space(.)="编辑"]`, chromedp.BySearch),
		chromedp.WaitVisible(`//button[contains(normalize-space(.),"返回备份任务")]`, chromedp.BySearch),
		chromedp.Click(`//nav[@aria-label="任务配置"]//button[normalize-space(.)="定时执行"]`, chromedp.BySearch),
		chromedp.Click(`//label[contains(normalize-space(.),"启用定时执行")]//input`, chromedp.BySearch),
		setSelectByLabel("执行频率", "weekly"),
		chromedp.WaitVisible(`//label[contains(normalize-space(.),"星期")]/select`, chromedp.BySearch),
		setSelectByLabel("执行频率", "interval"),
		chromedp.WaitVisible(`input[type="number"][max="8760"]`, chromedp.ByQuery),
		chromedp.Click(`//button[contains(normalize-space(.),"返回备份任务")]`, chromedp.BySearch),
		waitHeading("备份任务"),
	)
	t.Log("task schedule controls complete")
	runBrowser(t, ctx,
		clickPage("数据生命周期"), waitSelectedTab("数据生命周期"), clickButton("立即清理"),
		chromedp.WaitVisible(`//*[@role="dialog" and .//*[normalize-space(.)="确认清理执行数据"]]`, chromedp.BySearch),
	)
	t.Log("lifecycle preview visible")
	runBrowser(t, ctx,
		chromedp.SendKeys(`//*[@role="dialog"]//input[@autocomplete="current-password"]`, "correct horse battery staple", chromedp.BySearch),
		clickButton("确认清理"),
		chromedp.WaitNotPresent(`//*[@role="dialog" and .//*[normalize-space(.)="确认清理执行数据"]]`, chromedp.BySearch),
		chromedp.WaitVisible(`//p[contains(normalize-space(.),"上次清理")]`, chromedp.BySearch),
	)
	t.Log("task scheduling and lifecycle UI complete")

	// Configure restart locking, restart the production process, and unlock through React.
	const vaultPassphrase = "independent vault passphrase"
	t.Log("exercise vault restart UI")
	runBrowser(t, ctx,
		clickPage("安全设置"), waitSelectedTab("安全设置"),
		chromedp.SendKeys(`input[name="passphrase"]`, vaultPassphrase, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="passphraseConfirmation"]`, vaultPassphrase, chromedp.ByQuery),
		clickButton("启用重启后锁定"),
		chromedp.WaitVisible(`//*[contains(normalize-space(.),"已启用重启后锁定")]`, chromedp.BySearch),
	)
	process.Stop(t)
	process = startProductionBinary(t, binary, dataDir, listenerAddress)
	waitForHealth(t, baseURL)
	runBrowser(t, ctx,
		chromedp.Navigate(baseURL), waitHeading("解锁秘密库"),
		chromedp.SendKeys(`input[name="passphrase"]`, vaultPassphrase, chromedp.ByQuery),
		clickButton("解锁"), waitHeading("仪表盘"),
	)
	// Guard the supported compact widths against document-level horizontal overflow.
	for _, width := range []int64{320, 390, 768, 820} {
		var overflows bool
		runBrowser(t, ctx,
			chromedp.EmulateViewport(width, 900),
			chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth || document.body.scrollWidth > window.innerWidth`, &overflows),
		)
		if overflows {
			t.Fatalf("browser layout overflows horizontally at width %d", width)
		}
	}
	t.Log("responsive widths complete")
	recordCheck("real-browser-administration", "passed", filepath.Base(chrome))
}

func runBrowser(t *testing.T, ctx context.Context, actions ...chromedp.Action) {
	t.Helper()
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatal(err)
	}
}

func runBrowserDiagnostic(t *testing.T, ctx context.Context, process *productionProcess, label string, actions ...chromedp.Action) {
	t.Helper()
	stepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(stepCtx, actions...); err != nil {
		var body string
		_ = chromedp.Run(ctx, chromedp.Text("body", &body, chromedp.ByQuery))
		logBody, _ := os.ReadFile(process.logFile.Name())
		t.Fatalf("%s: %v\npage=%s\nservice log=%s", label, err, body, logBody)
	}
}

func clickButton(label string) chromedp.Action {
	return chromedp.Click(fmt.Sprintf(`//button[normalize-space(.)=%q]`, label), chromedp.BySearch)
}

func clickNavigation(label string) chromedp.Action {
	return chromedp.Click(fmt.Sprintf(`//nav//button[contains(normalize-space(.),%q)]`, label), chromedp.BySearch)
}

func clickPage(label string) chromedp.Action {
	group := navigationGroup(label)
	if group == "" {
		return clickNavigation(label)
	}
	return chromedp.Tasks{
		clickNavigation(group),
		chromedp.WaitVisible(fmt.Sprintf(`//*[@role="tablist"]//button[normalize-space(.)=%q]`, label), chromedp.BySearch),
		chromedp.Click(fmt.Sprintf(`//*[@role="tablist"]//button[normalize-space(.)=%q]`, label), chromedp.BySearch),
	}
}

func navigationGroup(label string) string {
	switch label {
	case "远程主机", "Agent 节点", "数据库实例":
		return "连接管理"
	case "运行记录", "告警历史", "投递记录", "审计日志":
		return "活动与记录"
	case "兼容性中心", "通知配置", "Agent 服务", "安全设置", "配置备份与恢复", "数据生命周期":
		return "系统"
	}
	return ""
}

func waitSelectedTab(label string) chromedp.Action {
	return chromedp.WaitVisible(fmt.Sprintf(`//*[@role="tablist"]//button[@aria-selected="true" and normalize-space(.)=%q]`, label), chromedp.BySearch)
}

func waitNavigation(label string) chromedp.Action {
	return chromedp.WaitVisible(fmt.Sprintf(`//nav//button[contains(@class,"selected") and contains(normalize-space(.),%q)]`, label), chromedp.BySearch)
}

func waitHeading(label string) chromedp.Action {
	return chromedp.WaitVisible(fmt.Sprintf(`//h1[normalize-space(.)=%q]`, label), chromedp.BySearch)
}

func waitForBrowserDownload(t *testing.T, directory, filename string, timeout time.Duration) []byte {
	t.Helper()
	path := filepath.Join(directory, filename)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && len(content) > 0 {
			return content
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("browser download %s did not complete", filename)
	return nil
}

func waitForBackgroundCapacity(t *testing.T, baseURL, repositoryID string, timeout time.Duration) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/login", "", map[string]any{
		"username": "admin", "password": "correct horse battery staple",
	}, http.StatusOK)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories/"+repositoryID+"/capacity-policy", "", nil)
		var policy struct {
			LastSuccessAt string `json:"lastSuccessAt"`
			LastError     string `json:"lastError"`
		}
		if json.Unmarshal(body, &policy) == nil && policy.LastSuccessAt != "" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("background capacity probe did not succeed for repository %s", repositoryID)
}

func setFirstDialogSelect(value string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`(() => { const e = document.querySelector('form[role="dialog"] select'); e.value = %q; e.dispatchEvent(new Event('change', {bubbles:true})); })()`, value), nil)
}

func setSelectByLabel(label, value string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`(() => { const label = [...document.querySelectorAll('label')].find((item) => item.textContent.includes(%q)); const select = label?.querySelector('select'); if (!select) throw new Error('select not found'); select.value = %q; select.dispatchEvent(new Event('change', {bubbles:true})); })()`, label, value), nil)
}

func setControlledValue(selector, value string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`(() => {
		const element = document.querySelector(%q);
		if (!element) throw new Error('controlled input not found');
		const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
		Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, %q);
		element.dispatchEvent(new Event('input', {bubbles:true}));
		element.dispatchEvent(new Event('change', {bubbles:true}));
	})()`, selector, value), nil)
}

func selectFirstNonEmptyByLabel(label string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`(() => { const label = [...document.querySelectorAll('label')].find((item) => item.textContent.includes(%q)); const select = label?.querySelector('select'); const option = [...(select?.options ?? [])].find((item) => item.value); if (!select || !option) throw new Error('select option not found'); select.value = option.value; select.dispatchEvent(new Event('change', {bubbles:true})); })()`, label), nil)
}

func prepareBrowserDirectorySnapshot(t *testing.T, baseURL string) (string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(source, "album"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "album", "payload.txt"), []byte("browser restore payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	_, headers := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/login", "", map[string]any{"username": "admin", "password": "correct horse battery staple"}, http.StatusOK)
	csrf := headers.Get("X-CSRF-Token")
	repositoryBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/repositories", csrf, map[string]any{
		"name": "浏览器恢复仓库", "kind": "local", "path": filepath.Join(t.TempDir(), "repository"),
		"password": "browser-repository-password", "passwordConfirmed": true,
	})
	repositoryID := jsonID(t, repositoryBody)
	initializeBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+repositoryID+"/initialize", csrf, map[string]any{}, http.StatusAccepted)
	var initialize struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(initializeBody, &initialize); err != nil {
		t.Fatal(err)
	}
	waitApplicationOperation(t, client, baseURL, csrf, initialize.OperationID)
	taskPayload := map[string]any{
		"name": "浏览器恢复任务", "kind": "directory", "repositoryId": repositoryID,
		"directory": map[string]any{"path": source, "exclusions": []string{}, "skipIfUnchanged": false},
		"retention": map[string]any{"keepLast": 3}, "resources": map[string]any{"compression": "auto"}, "enabled": false,
	}
	taskBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks", csrf, taskPayload)
	taskID := jsonID(t, taskBody)
	previewBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/preview", csrf, map[string]any{})
	var preview struct {
		PreviewID string `json:"previewId"`
	}
	if err := json.Unmarshal(previewBody, &preview); err != nil || preview.PreviewID == "" {
		t.Fatalf("scope preview=%s err=%v", previewBody, err)
	}
	taskPayload["enabled"] = true
	taskPayload["previewId"] = preview.PreviewID
	requestApplicationStatus(t, client, http.MethodPut, baseURL+"/api/tasks/"+taskID, csrf, taskPayload, http.StatusOK)
	runBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/run", csrf, map[string]any{}, http.StatusAccepted)
	var run struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(runBody, &run); err != nil {
		t.Fatal(err)
	}
	waitApplicationOperation(t, client, baseURL, csrf, run.OperationID)
	runsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/runs?limit=10", csrf, nil)
	var runs []struct {
		ID     string `json:"id"`
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(runsBody, &runs); err != nil {
		t.Fatal(err)
	}
	runID := ""
	for _, candidate := range runs {
		if candidate.TaskID == taskID {
			runID = candidate.ID
			break
		}
	}
	if runID == "" {
		t.Fatalf("task run missing from history: %s", runsBody)
	}
	detailBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/runs/"+runID, csrf, nil)
	var detail struct {
		Summary struct {
			ScopeConfirmation struct {
				PreviewID string `json:"previewId"`
			} `json:"scopeConfirmation"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(detailBody, &detail); err != nil || detail.Summary.ScopeConfirmation.PreviewID != preview.PreviewID {
		t.Fatalf("run scope confirmation=%s err=%v", detailBody, err)
	}
	verifyDisabledNotificationPreservesFailedRun(t, client, baseURL, csrf, taskID, source)
	return repositoryID, filepath.Join(t.TempDir(), "restored")
}

func verifyDisabledNotificationPreservesFailedRun(t *testing.T, client *http.Client, baseURL, csrf, taskID, source string) {
	t.Helper()
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received.Add(1)
	}))
	defer receiver.Close()
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/ntfy", csrf, map[string]any{
		"baseUrl": receiver.URL, "topic": "disabled-e2e", "enabled": false,
	}, http.StatusOK)
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/ntfy/test", csrf, map[string]any{}, http.StatusConflict)

	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	runBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/run", csrf, map[string]any{}, http.StatusAccepted)
	var run struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(runBody, &run); err != nil || run.OperationID == "" {
		t.Fatalf("failed-run operation=%s err=%v", runBody, err)
	}
	waitApplicationOperationState(t, client, baseURL, csrf, run.OperationID, "failed")

	runsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/runs?limit=10", csrf, nil)
	var runs []struct {
		TaskID string `json:"taskId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(runsBody, &runs); err != nil {
		t.Fatal(err)
	}
	status := ""
	for _, candidate := range runs {
		if candidate.TaskID == taskID {
			status = candidate.Status
			break
		}
	}
	if status != "failed" {
		t.Fatalf("failed run status=%q runs=%s", status, runsBody)
	}

	alertsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/alerts?limit=20", csrf, nil)
	var health struct {
		Active []struct {
			StateKey string `json:"stateKey"`
		} `json:"active"`
		Deliveries []struct {
			StateKey string `json:"stateKey"`
			Status   string `json:"status"`
			Attempt  int    `json:"attempt"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal(alertsBody, &health); err != nil {
		t.Fatal(err)
	}
	wantStateKey := "task:" + taskID + ":run"
	active, skipped := false, false
	for _, alert := range health.Active {
		active = active || alert.StateKey == wantStateKey
	}
	for _, delivery := range health.Deliveries {
		skipped = skipped || delivery.StateKey == wantStateKey && delivery.Status == "skipped_disabled" && delivery.Attempt == 0
	}
	if !active || !skipped {
		t.Fatalf("disabled notification health=%s", alertsBody)
	}
	if received.Load() != 0 {
		t.Fatalf("disabled ntfy received %d real requests", received.Load())
	}
}

func productionBinary(t *testing.T) string {
	t.Helper()
	_, currentFile, _, _ := runtime.Caller(0)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	binary := os.Getenv("RESTIC_CONTROL_E2E_BINARY")
	if binary == "" {
		binary = filepath.Join(repositoryRoot, "dist", "shadoc")
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		t.Fatalf("production E2E binary is missing at %s; run make build first", binary)
	}
	return binary
}

func chromeExecutable(t *testing.T) string {
	t.Helper()
	for _, path := range []string{
		os.Getenv("RESTIC_CONTROL_E2E_CHROME"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser",
	} {
		if info, err := os.Stat(path); path != "" && err == nil && !info.IsDir() {
			return path
		}
	}
	t.Fatal("real browser E2E requires Chrome/Chromium; set RESTIC_CONTROL_E2E_CHROME")
	return ""
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}
