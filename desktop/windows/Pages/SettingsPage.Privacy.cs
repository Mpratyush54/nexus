using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using Nexus.Core.Diagnostics;

namespace Nexus.Pages;

// Settings > Privacy & diagnostics: crash-report consent, "Report a
// problem", the logs folder and reports still waiting to upload.
public sealed partial class SettingsPage
{
    private readonly ToggleSwitch _autoReports = CompactSwitch();
    private readonly TextBlock _reportQueue = Muted("");
    private readonly Button _sendQueued = new() { Content = "Send now", Visibility = Visibility.Collapsed };
    private bool _autoReportsSync;

    private UIElement BuildPrivacySection()
    {
        _autoReportsSync = true;
        _autoReports.IsOn = AppPrefs.Data.AutoSendCrashReports;
        _autoReportsSync = false;
        _autoReports.Toggled += (_, _) =>
        {
            if (_autoReportsSync) return;
            AppPrefs.Data.AutoSendCrashReports = _autoReports.IsOn;
            AppPrefs.Save();
            DiagLog.Info("settings", "automatic crash reports " + (_autoReports.IsOn ? "on" : "off"));
        };
        AutomationProperties.SetName(_autoReports, "Send crash reports automatically");

        var report = new Button { Content = "Report a problem…", Style = AccentStyle() };
        report.Click += (_, _) => MainWindow.Current?.ShowReportProblem();
        var openLogs = new Button { Content = "Open logs folder" };
        openLogs.Click += (_, _) => AppDiagnostics.OpenLogsFolder();
        _sendQueued.Click += async (_, _) =>
        {
            _sendQueued.IsEnabled = false;
            await Call("diagnostics.reports.flush", new { force = true });
            _sendQueued.IsEnabled = true;
            await LoadPrivacyAsync();
        };

        var crash = SettingsGroup("Crash reports",
            SettingRow(RowText("Send crash reports automatically",
                "If Nexus crashes, it saves a short report on this computer. Off (the default): the next time Nexus starts it asks you, " +
                "and shows exactly what would be sent. On: reports are sent without asking, and without your account."), _autoReports),
            SettingRow(RowText("What a report contains", ReportDialogs.WhatIsSent)),
            SettingRow(RowText("Reports waiting to send", _reportQueue), _sendQueued));
        var help = SettingsGroup("Get help",
            SettingRow(RowText("Report a problem",
                "Describe what went wrong. You'll see a preview of everything that's sent, and get a report ID to quote to support."), report),
            SettingRow(RowText("Logs", "Nexus keeps its last few days of logs on this computer (" + AppDiagnostics.LogsDir + "). Tokens and email addresses are removed before anything is written."), openLogs));
        return new StackPanel { Children = { crash, help } };
    }

    private static Style? AccentStyle() =>
        Application.Current.Resources.TryGetValue("AccentButtonStyle", out var value) ? value as Style : null;

    private async Task LoadPrivacyAsync()
    {
        _autoReportsSync = true;
        _autoReports.IsOn = AppPrefs.Data.AutoSendCrashReports;
        _autoReportsSync = false;
        var response = await Call("diagnostics.reports.status");
        var pending = response.Ok && response.Result is { } status && int.TryParse(NxJson.Text(status, "pending"), out var n)
            ? n : ReportStore.PendingCount(AppDiagnostics.BaseDir);
        _reportQueue.Text = pending == 0
            ? "Nothing waiting. Reports that can't be sent right away are retried automatically."
            : $"{pending} report{(pending == 1 ? "" : "s")} waiting to send. Nexus retries automatically while you're online.";
        _sendQueued.Visibility = pending > 0 ? Visibility.Visible : Visibility.Collapsed;
    }
}
