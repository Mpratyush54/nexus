package store

import (
	"testing"
)

func TestMemStoreOrganizations(t *testing.T) {
	s := NewMemStore()
	ctx := t.Context()

	o, err := s.CreateOrganization(ctx, "Acme", "acme", "alice")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if o.ID == "" || o.CreatedBy != "alice" {
		t.Fatalf("org = %+v", o)
	}

	role, err := s.GetOrgMemberRole(ctx, o.ID, "alice")
	if err != nil || role != OrgRoleAdmin {
		t.Fatalf("creator role = %q err=%v", role, err)
	}

	if _, err := s.CreateOrganization(ctx, "Other", "acme", "bob"); err == nil {
		t.Fatal("duplicate slug should conflict")
	}

	if _, err := s.AddOrgMember(ctx, o.ID, "bob", "MEMBER", "alice"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	if _, err := s.AddOrgMember(ctx, o.ID, "carol", "ADMIN", "alice"); err != nil {
		t.Fatalf("AddOrgMember carol: %v", err)
	}

	p, err := s.CreateOrgProject(ctx, o.ID, "nexus", "Nexus", "git@github.com:Acme/nexus.git", "abc", "alice")
	if err != nil {
		t.Fatalf("CreateOrgProject: %v", err)
	}
	if p.OrgID != o.ID || p.CanonicalURL != "github.com/acme/nexus" {
		t.Fatalf("project = %+v", p)
	}

	ok, err := s.IsProjectMember(ctx, "carol", p.ID)
	if err != nil || !ok {
		t.Fatalf("org admin project access: ok=%v err=%v", ok, err)
	}
	ok, err = s.IsProjectMember(ctx, "bob", p.ID)
	if err != nil || ok {
		t.Fatalf("org member must not inherit: ok=%v err=%v", ok, err)
	}

	list, err := s.ListOrganizations(ctx, "bob")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListOrganizations bob: %v %+v", err, list)
	}
	projects, err := s.ListOrgProjects(ctx, o.ID)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListOrgProjects: %v %+v", err, projects)
	}

	// alice and carol are admins; removing alice is fine, last admin is not.
	if err := s.RemoveOrgMember(ctx, o.ID, "alice"); err != nil {
		t.Fatalf("RemoveOrgMember alice: %v", err)
	}
	if err := s.RemoveOrgMember(ctx, o.ID, "carol"); err == nil {
		t.Fatal("expected conflict removing last admin")
	}
}

func TestNormalizeOrgRole(t *testing.T) {
	r, err := NormalizeOrgRole("")
	if err != nil || r != OrgRoleMember {
		t.Fatalf("empty = %q %v", r, err)
	}
	r, err = NormalizeOrgRole("admin")
	if err != nil || r != OrgRoleAdmin {
		t.Fatalf("admin = %q %v", r, err)
	}
	if _, err := NormalizeOrgRole("OWNER"); err == nil {
		t.Fatal("OWNER should be invalid for orgs")
	}
}
