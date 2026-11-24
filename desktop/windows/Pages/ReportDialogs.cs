using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core.Diagnostics;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

/// <summary>
/// The two places a person decides what Nexus sends: the "Nexus closed
/// unexpectedly" prompt on the next launch, and "Report a problem" (Settings
/// and the tray menu). Both show the exact JSON that would be sent.
/// </summary>
internal static class ReportDialogs
{
    public const string WhatIsSent =
        "A report has the error, where in Nexus's code it happened, your Nexus and Windows versions, and recent lines from Nexus's own log. " +
        "It never has your chats, memories, prompts, files or sign-in tokens. Email addresses and your Windows user name are removed before anything leaves this computer.";

    private static bool s_open;

    // Read-only, selectable JSON in a scroller (a read-only TextBox drops
    // everything after the first line feed).
    private static TextBlock Preview(string json) => new()
    {
        Text = json,
        IsTextSelectionEnabled = true,
        TextWrapping = TextWrapping.NoWrap,
        FontFamily = new FontFamily("Cascadia Mono, Consolas"),
        FontSize = 11.5,
        LineHeight = 16,
    };

    private static Expander PreviewExpander(TextBlock preview, bool open) => new()
    {
        Header = "Preview exactly what's sent",
        Content = new ScrollViewer
        {
            Content = preview, MaxHeight = 260, Padding = new Thickness(0, 0, 12, 12),
            HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, HorizontalScrollMode = ScrollMode.Enabled,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
        },
        IsExpanded = open,
        HorizontalAlignment = HorizontalAlignment.Stretch,
        HorizontalContentAlignment = HorizontalAlignment.Stretch,
    };

    private static TextBlock Para(string text, double opacity = 1) => new()
    {
        Text = text, TextWrapping = TextWrapping.Wrap, FontSize = NexusTokens.FontBody, Opacity = opacity
    };

    /// On launch: offers the reports a crash left behind. With "send
    /// automatically" on, they go without asking (anonymously).
    public static async Task OfferHeldReportsAsync(XamlRoot root)
    {
        var held = AppDiagnostics.HeldReports();
        if (held.Count == 0) return;
        if (AppPrefs.Data.AutoSendCrashReports)
        {
            DiagLog.Info("report", $"sending {held.Count} crash report(s) automatically");
            AppDiagnostics.Release(held, signed: false);
            return;
        }
        if (s_open) return;
        s_open = true;
        try
        {
            var crashed = held.Any(r => r.Kind == ProblemReport.KindCrash);
            var json = held.Count == 1 ? held[0].ToJson(indented: true) : "[\n" + string.Join(",\n", held.Select(r => r.ToJson(indented: true))) + "\n]";
            var always = new CheckBox { Content = "Always send crash reports automatically" };
            AutomationProperties.SetHelpText(always, "You can change this in Settings, Privacy & diagnostics.");
            var body = new StackPanel { Spacing = 12, MaxWidth = 560 };
            body.Children.Add(Para(held.Count > 1
                ? $"Nexus saved {held.Count} problem reports. Sending them helps us find and fix what went wrong."
                : "Nexus saved a report about what went wrong. Sending it helps us find and fix the problem."));
            body.Children.Add(Para(WhatIsSent, 0.7));
            body.Children.Add(PreviewExpander(Preview(json), open: false));
            body.Children.Add(always);
            var dialog = new ContentDialog
            {
                Title = crashed ? "Nexus closed unexpectedly. Send a report?" : "Nexus ran into a problem last time. Send a report?",
                Content = new ScrollViewer { Content = body, MaxHeight = 560, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled },
                PrimaryButtonText = "Send",
                CloseButtonText = "Don't send",
                DefaultButton = ContentDialogButton.Primary,
                XamlRoot = root,
            };
            var choice = await dialog.ShowThemedAsync();
            if (always.IsChecked == true)
            {
                AppPrefs.Data.AutoSendCrashReports = true;
                AppPrefs.Save();
            }
            if (choice == ContentDialogResult.Primary)
            {
                DiagLog.Info("report", $"person chose to send {held.Count} crash report(s)");
                AppDiagnostics.Release(held, signed: true);
            }
            else
            {
                DiagLog.Info("report", $"person chose not to send {held.Count} crash report(s)");
                AppDiagnostics.Discard(held);
            }
        }
        catch (Exception ex)
        {
            DiagLog.Warn("report", "crash prompt failed", ex);
        }
        finally
        {
            s_open = false;
        }
    }

    /// "Report a problem": a note, recent logs (optional), a live preview of
    /// the exact report, Send, and a confirmation with the report ID.
    public static async Task ShowReportProblemAsync(XamlRoot root)
    {
        if (s_open) return;
        s_open = true;
        try
        {
            var reportId = ProblemReport.NewReportId();
            var note = new TextBox
            {
                Header = "What happened?",
                PlaceholderText = "What were you doing, and what did you expect to happen? Don't include passwords or private code.",
                AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, MinHeight = 96, MaxHeight = 180, MaxLength = ProblemReport.MaxUserNoteChars,
            };
            var logs = new CheckBox { Content = "Include recent logs (the last 200 lines from Nexus and its engine)", IsChecked = true };
            var preview = Preview("");
            ProblemReport Build()
            {
                var report = AppDiagnostics.UserReport(note.Text, logs.IsChecked == true);
                report.ReportId = reportId;
                return report;
            }
            var refresh = Microsoft.UI.Dispatching.DispatcherQueue.GetForCurrentThread().CreateTimer();
            refresh.Interval = TimeSpan.FromMilliseconds(300);
            refresh.IsRepeating = false;
            refresh.Tick += (_, _) => preview.Text = Build().ToJson(indented: true);
            note.TextChanged += (_, _) => { refresh.Stop(); refresh.Start(); };
            logs.Checked += (_, _) => preview.Text = Build().ToJson(indented: true);
            logs.Unchecked += (_, _) => preview.Text = Build().ToJson(indented: true);
            preview.Text = Build().ToJson(indented: true);

            var body = new StackPanel { Spacing = 12, MaxWidth = 560 };
            body.Children.Add(Para("Tell us what went wrong. The report goes to the Nexus team with your account, so we can follow up.", 0.85));
            body.Children.Add(note);
            body.Children.Add(logs);
            body.Children.Add(Para(WhatIsSent, 0.7));
            body.Children.Add(PreviewExpander(preview, open: true));
            var dialog = new ContentDialog
            {
                Title = "Report a problem",
                Content = new ScrollViewer { Content = body, MaxHeight = 600, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled },
                PrimaryButtonText = "Send",
                SecondaryButtonText = "Open logs folder",
                CloseButtonText = "Cancel",
                DefaultButton = ContentDialogButton.Primary,
                IsPrimaryButtonEnabled = false,
                XamlRoot = root,
            };
            note.TextChanged += (_, _) => dialog.IsPrimaryButtonEnabled = note.Text.Trim().Length > 0;
            dialog.SecondaryButtonClick += (_, args) =>
            {
                args.Cancel = true; // keep the dialog open
                AppDiagnostics.OpenLogsFolder();
            };
            AutomationProperties.SetName(note, "What happened?");
            if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;

            var outcome = await AppDiagnostics.SendAsync(Build(), signed: true);
            await ShowConfirmationAsync(root, outcome);
        }
        catch (Exception ex)
        {
            DiagLog.Warn("report", "report dialog failed", ex);
        }
        finally
        {
            s_open = false;
        }
    }

    private static async Task ShowConfirmationAsync(XamlRoot root, AppDiagnostics.SendOutcome outcome)
    {
        var body = new StackPanel { Spacing = 10, MaxWidth = 520 };
        string title;
        if (outcome.Sent)
        {
            title = "Thanks, your report was sent";
            body.Children.Add(Para("If you contact support about this, quote the report ID:"));
        }
        else if (outcome.Queued)
        {
            title = "Report saved";
            body.Children.Add(Para("Nexus couldn't reach the server right now, so the report will be sent automatically when you're back online." +
                (outcome.Error.Length > 0 ? " (" + outcome.Error + ")" : "")));
            body.Children.Add(Para("Report ID:"));
        }
        else
        {
            title = "Couldn't send the report";
            body.Children.Add(Para(outcome.Error.Length > 0 ? outcome.Error : "The report could not be saved."));
        }
        var id = new TextBlock
        {
            Text = outcome.ReportId, IsTextSelectionEnabled = true, FontFamily = new FontFamily("Cascadia Mono, Consolas"),
            FontSize = NexusTokens.FontSection
        };
        AutomationProperties.SetName(id, "Report ID " + outcome.ReportId);
        if (outcome.Sent || outcome.Queued) body.Children.Add(id);
        if (outcome.RequestId.Length > 0) body.Children.Add(Para("Server ref " + outcome.RequestId, 0.6));
        var dialog = new ContentDialog
        {
            Title = title,
            Content = body,
            PrimaryButtonText = outcome.Sent || outcome.Queued ? "Copy report ID" : "",
            CloseButtonText = "Done",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = root,
        };
        if (await dialog.ShowThemedAsync() == ContentDialogResult.Primary)
        {
            var data = new DataPackage();
            data.SetText(outcome.ReportId);
            Clipboard.SetContent(data);
        }
    }
}
