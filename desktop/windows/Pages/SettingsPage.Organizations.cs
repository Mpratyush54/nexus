using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using System.Text.Json;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

public sealed partial class SettingsPage
{
    private readonly ComboBox _orgChoice = new() { PlaceholderText = "Choose an organization", MinWidth = 220 };
    private readonly StackPanel _orgDetails = new();
    private readonly TextBlock _orgStatus = new() { TextWrapping = TextWrapping.Wrap, FontSize = 12, Opacity = 0.65, Margin = new Thickness(0, 10, 0, 0) };
    private string _orgUser = "";
    private bool _orgLoading;
    private int _orgRequest;
    // Invite list deliberately redacts bearer codes. Retain only codes created
    // in this signed-in UI session; never persist them in preferences/logs.
    private readonly Dictionary<string, string> _createdInviteCodes = new();

    private void ClearOrganizationState()
    {
        ++_orgRequest;
        _orgLoading = true;
        _orgChoice.Items.Clear();
        _orgLoading = false;
        _orgDetails.Children.Clear();
        _createdInviteCodes.Clear();
        _orgUser = "";
        _orgStatus.Text = "Sign in to manage organizations and invitations.";
    }

    private UIElement BuildOrganizationSection()
    {
        var create = OrgAction("Create organization", CreateOrganizationAsync);
        var join = OrgAction("Join with invitation", JoinOrganizationAsync);
        var refresh = OrgAction("Refresh", RefreshOrganizationsAsync);
        _orgChoice.SelectionChanged += async (_, _) =>
        {
            if (!_orgLoading) await LoadOrganizationAsync();
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_orgChoice, "Organization");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(refresh, "Refresh organizations");
        // Loaded when the section opens (the shell calls RefreshOrganizationsAsync).
        var section = SettingsGroup("Organizations & members",
            SettingRow(RowText("Organization", "Manage your teams, invitations, and member roles. Team-shared chats remain live; private snapshot shares stay version-limited."), Row(_orgChoice, refresh)),
            SettingRow(RowText("Create or join", "Start a new team, or paste an invitation code a teammate shared."), Row(create, join)));
        return new StackPanel { Children = { section, _orgStatus, _orgDetails } };
    }

    // Keep async button errors inside Settings instead of the app's global handler.
    private Button OrgAction(string label, Func<Task> action)
    {
        var button = new Button { Content = label };
        button.Click += async (_, _) =>
        {
            button.IsEnabled = false;
            try { await action(); }
            catch (Exception ex) { Notify(NxErrors.ForException("That didn't work. Try again.", ex, "orgs"), true); }
            finally { button.IsEnabled = true; }
        };
        return button;
    }

    private string _orgUserName = "";

    private string SelectedOrganization => (_orgChoice.SelectedItem as ComboBoxItem)?.Tag as string ?? "";

    private async Task RefreshOrganizationsAsync()
    {
        if (_orgLoading) return;
        _orgLoading = true;
        var request = ++_orgRequest;
        var selected = SelectedOrganization;
        _orgStatus.Text = "Loading organizations…";
        _orgDetails.Children.Clear();
        try
        {
            var auth = await Call(NxMethods.AuthStatus);
            if (request != _orgRequest) return;
            if (!auth.Ok || auth.Result is not { } account || NxJson.Text(account, "signed_in") != "true")
            {
                _orgChoice.Items.Clear();
                _orgUser = "";
                _createdInviteCodes.Clear();
                _orgStatus.Text = "Sign in to manage organizations and invitations.";
                return;
            }
            var user = NxJson.Text(account, "user_id");
            _orgUserName = NxJson.Text(account, "username");
            if (_orgUser != user) _createdInviteCodes.Clear();
            _orgUser = user;
            var response = await Call(NxMethods.OrgsList);
            if (request != _orgRequest) return;
            _orgChoice.Items.Clear();
            if (!response.Ok) { _orgStatus.Text = response.Error ?? "Could not load organizations."; return; }
            ComboBoxItem? next = null;
            foreach (var org in NxJson.Items(response.Result))
            {
                var id = NxJson.Text(org, "id");
                var choice = new ComboBoxItem { Content = NxJson.Text(org, "name"), Tag = id };
                _orgChoice.Items.Add(choice);
                if (id == selected) next = choice;
            }
            _orgChoice.SelectedItem = next ?? _orgChoice.Items.FirstOrDefault();
            _orgStatus.Text = _orgChoice.Items.Count == 0
                ? "No organizations yet. Create a team or accept an invitation to get started."
                : "";
        }
        finally { if (request == _orgRequest) _orgLoading = false; }
        if (SelectedOrganization.Length > 0) await LoadOrganizationAsync();
    }

    private static string RoleLabel(string role) => role.ToUpperInvariant() switch
    {
        "OWNER" => "Owner",
        "ADMIN" => "Admin",
        _ => "Member"
    };

    private async Task LoadOrganizationAsync()
    {
        var orgID = SelectedOrganization;
        var request = ++_orgRequest;
        _orgDetails.Children.Clear();
        if (orgID.Length == 0) return;
        _orgStatus.Text = "Loading members…";
        var response = await Call(NxMethods.OrgsGet, new { org_id = orgID });
        if (request != _orgRequest || orgID != SelectedOrganization) return;
        if (!response.Ok || response.Result is not { } detail)
        {
            _orgStatus.Text = response.Error ?? "Organization unavailable.";
            return;
        }
        var members = detail.TryGetProperty("members", out var list) ? NxJson.Items(list).ToList() : new List<JsonElement>();
        // Older servers list members by id only. Names then come from the
        // sharing recipients of an organization project (the same people,
        // named), and your own from your account.
        var names = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        if (members.Any(m => NxJson.Text(m, "name") is var n && (n.Length == 0 || n == NxJson.Text(m, "user_id"))))
        {
            var orgProject = detail.TryGetProperty("projects", out var orgProjects) ? NxJson.Items(orgProjects).Select(p => NxJson.Text(p, "id")).FirstOrDefault(id => id.Length > 0) ?? "" : "";
            if (orgProject.Length > 0)
            {
                var people = await Call(NxMethods.SharingRecipients, new { project_id = orgProject });
                if (request != _orgRequest) return;
                if (people.Ok && people.Result is { } peopleBody && peopleBody.TryGetProperty("people", out var personList))
                    foreach (var person in NxJson.Items(personList))
                        if (NxJson.Text(person, "name") is { Length: > 0 } personName && personName != NxJson.Text(person, "user_id"))
                            names[NxJson.Text(person, "user_id")] = personName;
            }
            if (_orgUserName.Length > 0 && _orgUser.Length > 0) names[_orgUser] = _orgUserName;
        }
        var ownRole = members.Where(m => NxJson.Text(m, "user_id") == _orgUser)
            .Select(m => NxJson.Text(m, "role")).FirstOrDefault() ?? "MEMBER";
        var owner = ownRole == "OWNER";
        var manages = owner || ownRole == "ADMIN";
        _orgStatus.Text = $"{Nexus.Core.FriendlyTime.Count(members.Count, "member")} · You are {RoleLabel(ownRole).ToLowerInvariant()}" + (_orgUser.Length == 0 ? " (sign in again to confirm your role)" : "");
        _orgDetails.Children.Add(GroupHeader("Members", 24));
        _orgDetails.Children.Add(SettingRow(
            RowText("Private chats stay private", "Organization membership does not automatically reveal private chats. Share a chat with the project team or named people to grant access."),
            manages ? OrgAction("Invite by email", () => InviteOrganizationAsync(orgID, owner)) : null));
        foreach (var member in members)
        {
            var userID = NxJson.Text(member, "user_id");
            var name = NxJson.Text(member, "name");
            if (name.Length == 0 || name == userID) name = names.GetValueOrDefault(userID, "");
            if (name.Length == 0) name = "Member";
            if (userID == _orgUser) name += " (you)";
            var role = NxJson.Text(member, "role");
            StackPanel? actions = null;
            if (manages && (owner || role != "OWNER"))
            {
                var label = name;
                var change = OrgAction("Change role", () => ChangeOrganizationRoleAsync(orgID, userID, label, role, owner));
                var remove = OrgAction("Remove member", () => RemoveOrganizationMemberAsync(orgID, userID, label));
                Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(change, "Change role for " + label);
                Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(remove, "Remove " + label);
                actions = Row(change, remove);
            }
            _orgDetails.Children.Add(SettingRow(RowText(name, RoleLabel(role)), actions));
        }
        if (detail.TryGetProperty("projects", out var projects))
        {
            _orgDetails.Children.Add(GroupHeader("Organization projects", 24));
            var rows = NxJson.Items(projects).ToList();
            if (rows.Count == 0) _orgDetails.Children.Add(NoteRow("No organization projects yet."));
            foreach (var project in rows)
            {
                var name = NxJson.Text(project, "display_name");
                if (name.Length == 0) name = NxJson.Text(project, "folder_name");
                _orgDetails.Children.Add(SettingRow(RowText(name)));
            }
        }
        if (!manages) return;
        var invites = await Call(NxMethods.OrgsInvites, new { org_id = orgID });
        if (request != _orgRequest || orgID != SelectedOrganization) return;
        _orgDetails.Children.Add(GroupHeader("Invitations", 24));
        if (!invites.Ok) { _orgDetails.Children.Add(NoteRow(invites.Error ?? "Invitations unavailable.")); return; }
        var pending = NxJson.Items(invites.Result).Where(i =>
            NxJson.Text(i, "accepted_at").Length == 0 && NxJson.Text(i, "revoked_at").Length == 0 &&
            (!DateTimeOffset.TryParse(NxJson.Text(i, "expires_at"), out var expiry) || expiry > DateTimeOffset.UtcNow)).ToList();
        if (pending.Count == 0) _orgDetails.Children.Add(NoteRow("No pending invitations."));
        foreach (var invite in pending)
        {
            var inviteID = NxJson.Text(invite, "id");
            var email = NxJson.Text(invite, "email");
            var token = _createdInviteCodes.GetValueOrDefault(inviteID, "");
            var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
            if (token.Length > 0) actions.Children.Add(OrgAction("Copy invitation code", () =>
            {
                var data = new DataPackage(); data.SetText(token); Clipboard.SetContent(data);
                Notify("Invitation code copied. Share it only with the intended member.");
                return Task.CompletedTask;
            }));
            actions.Children.Add(OrgAction("Revoke", async () =>
            {
                if (!await ConfirmOrgAsync("Revoke invitation?", $"{email} will no longer be able to use this invitation.", "Revoke")) return;
                var result = await Call(NxMethods.OrgsInviteRevoke, new { org_id = orgID, invite_id = inviteID });
                Notify(result.Ok ? "Invitation revoked." : result.Error ?? "Could not revoke invitation.", !result.Ok);
                await LoadOrganizationAsync();
            }));
            _orgDetails.Children.Add(SettingRow(RowText(email, NxJson.Text(invite, "role").ToLowerInvariant()), actions));
        }
    }

    // Forms submit on Enter; confirmations of destructive actions default to Cancel.
    private ContentDialog OrgDialog(string title, UIElement content, string action, bool form = false) => new()
    {
        Title = title, Content = content, PrimaryButtonText = action, CloseButtonText = "Cancel",
        DefaultButton = form ? ContentDialogButton.Primary : ContentDialogButton.Close, XamlRoot = XamlRoot
    };

    private async Task<bool> ConfirmOrgAsync(string title, string text, string action) =>
        await OrgDialog(title, Line(text), action).ShowThemedAsync() == ContentDialogResult.Primary;

    private async Task CreateOrganizationAsync()
    {
        var name = new TextBox { Header = "Organization name", PlaceholderText = "Your team name", MaxLength = 120 };
        var dialog = OrgDialog("Create organization", name, "Create", form: true);
        dialog.IsPrimaryButtonEnabled = false;
        name.TextChanged += (_, _) => dialog.IsPrimaryButtonEnabled = !string.IsNullOrWhiteSpace(name.Text);
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;
        var result = await Call(NxMethods.OrgsCreate, new { name = name.Text.Trim() });
        Notify(result.Ok ? "Organization created. You are its owner." : result.Error ?? "Could not create organization.", !result.Ok);
        if (result.Ok) await RefreshOrganizationsAsync();
    }

    private async Task JoinOrganizationAsync()
    {
        var token = new TextBox { Header = "Invitation code", PlaceholderText = "Paste the code your team shared", TextWrapping = TextWrapping.Wrap };
        var dialog = OrgDialog("Join an organization", token, "Join", form: true);
        dialog.IsPrimaryButtonEnabled = false;
        token.TextChanged += (_, _) => dialog.IsPrimaryButtonEnabled = !string.IsNullOrWhiteSpace(token.Text);
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;
        var result = await Call(NxMethods.OrgsInviteAccept, new { token = token.Text.Trim() });
        Notify(result.Ok ? "You joined the organization." : result.Error ?? "Could not accept invitation.", !result.Ok);
        if (result.Ok) await RefreshOrganizationsAsync();
    }

    private static ComboBox OrganizationRoles(bool owner, string selected = "MEMBER")
    {
        var roles = new ComboBox { Header = "Role", HorizontalAlignment = HorizontalAlignment.Stretch };
        foreach (var role in owner ? new[] { "MEMBER", "ADMIN", "OWNER" } : new[] { "MEMBER", "ADMIN" }) roles.Items.Add(role);
        roles.SelectedItem = selected;
        return roles;
    }

    private async Task InviteOrganizationAsync(string orgID, bool owner)
    {
        var email = new TextBox { Header = "Email address", PlaceholderText = "teammate@example.com", MaxLength = 254 };
        var role = OrganizationRoles(owner);
        var dialog = OrgDialog("Invite a teammate", new StackPanel { Spacing = 12, Children = { email, role } }, "Create invitation", form: true);
        dialog.IsPrimaryButtonEnabled = false;
        email.TextChanged += (_, _) => dialog.IsPrimaryButtonEnabled =
            System.Net.Mail.MailAddress.TryCreate(email.Text.Trim(), out var address) && address.Address == email.Text.Trim();
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;
        var result = await Call(NxMethods.OrgsInviteCreate, new { org_id = orgID, email = email.Text.Trim(), role = role.SelectedItem as string ?? "MEMBER" });
        if (result.Ok && result.Result is { } created)
        {
            var code = NxJson.Text(created, "token");
            var id = NxJson.Text(created, "id");
            if (id.Length > 0 && code.Length > 0) _createdInviteCodes[id] = code;
        }
        Notify(result.Ok ? "Invitation created. You can copy its code below; email delivery depends on the server mail configuration." : result.Error ?? "Could not create invitation.", !result.Ok);
        if (result.Ok) await LoadOrganizationAsync();
    }

    private async Task ChangeOrganizationRoleAsync(string orgID, string userID, string name, string current, bool owner)
    {
        var role = OrganizationRoles(owner, current);
        if (await OrgDialog($"Change role for {name}", role, "Save role", form: true).ShowThemedAsync() != ContentDialogResult.Primary) return;
        var selected = role.SelectedItem as string ?? current;
        if (selected == current) return;
        var result = await Call(NxMethods.OrgsMemberRole, new { org_id = orgID, user_id = userID, role = selected });
        Notify(result.Ok ? "Member role updated." : result.Error ?? "Could not change member role.", !result.Ok);
        await LoadOrganizationAsync();
    }

    private async Task RemoveOrganizationMemberAsync(string orgID, string userID, string name)
    {
        if (!await ConfirmOrgAsync("Remove organization member?", $"Remove {name} from this organization? Private chat ownership is not transferred by this action.", "Remove member")) return;
        var result = await Call(NxMethods.OrgsMemberRemove, new { org_id = orgID, user_id = userID });
        Notify(result.Ok ? "Member removed." : result.Error ?? "Could not remove member.", !result.Ok);
        if (result.Ok) await RefreshOrganizationsAsync();
    }
}
