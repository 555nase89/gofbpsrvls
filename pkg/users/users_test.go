package users

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"gofbpsrvls/pkg/flow"
	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/pkg/fbp"
)

func setup(t *testing.T) (*store.Store, *flow.Runner, *flow.Runner) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	start := func(net *fbp.Network, err error) *flow.Runner {
		if err != nil {
			t.Fatal(err)
		}
		r, err := flow.Start(context.Background(), net, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Stop)
		return r
	}
	return st, start(Login(st)), start(Useradmin(st))
}

// call runs in through r and returns "" on Success or the error message.
func call(t *testing.T, r *flow.Runner, in any) string {
	t.Helper()
	outport, p, err := r.Call(context.Background(), fbp.In, in)
	if err != nil {
		t.Fatal(err)
	}
	if outport == fbp.Error {
		return flow.ErrorMessage(p)
	}
	return ""
}

func TestUseradminAndLogin(t *testing.T) {
	st, login, admin := setup(t)
	ctx := context.Background()
	for _, c := range []struct {
		in   UserInput
		want string
	}{
		{UserInput{Action: ActionCreate, Name: "anna", Password: "0123456789", Role: store.RoleAdmin}, ""},
		{UserInput{Action: ActionCreate, Name: "anna", Password: "0123456789", Role: store.RoleAdmin}, `user "anna" exists`},
		{UserInput{Action: ActionCreate, Name: "bob", Password: "short", Role: store.RoleViewer}, "password must have at least 10 characters"},
		{UserInput{Action: ActionCreate, Name: " bob", Password: "0123456789", Role: store.RoleViewer}, "name must not be empty or start or end with white space"},
		{UserInput{Action: ActionCreate, Name: "bob", Password: "0123456789", Role: "root"}, "role must be admin or viewer"},
		{UserInput{Action: "rename", Name: "bob"}, `unknown action "rename"`},
	} {
		if got := call(t, admin, c.in); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.in, got, c.want)
		}
	}
	if got := call(t, admin, UserInput{Action: ActionUpdate, Name: "anna", Role: store.RoleViewer}); got != "the last admin cannot be removed or demoted" {
		t.Fatalf("demote last admin: %q", got)
	}
	if got := call(t, admin, UserInput{Action: ActionDelete, Name: "anna"}); got != "the last admin cannot be removed or demoted" {
		t.Fatalf("delete last admin: %q", got)
	}

	if got := call(t, login, LoginInput{Name: "anna", Password: "wrong-password"}); got != ErrLogin.Error() {
		t.Fatalf("wrong password: %q", got)
	}
	if got := call(t, login, LoginInput{Name: "nobody", Password: "0123456789"}); got != ErrLogin.Error() {
		t.Fatalf("unknown user: %q", got)
	}
	outport, p, err := login.Call(ctx, fbp.In, LoginInput{Name: "anna", Password: "0123456789"})
	if err != nil || outport != fbp.Success {
		t.Fatalf("login: %s %s %v", outport, p.Data, err)
	}
	res, _ := fbp.DataAs[LoginResult](p)
	if u, err := st.SessionUser(ctx, res.Token); err != nil || u.Name != "anna" || res.User.Role != store.RoleAdmin {
		t.Fatalf("session: %+v %v", u, err)
	}

	// A second admin makes demoting and deleting the first possible; the
	// new password ends anna's session.
	if got := call(t, admin, UserInput{Action: ActionCreate, Name: "carl", Password: "abcdefghij", Role: store.RoleAdmin}); got != "" {
		t.Fatal(got)
	}
	if got := call(t, admin, UserInput{Action: ActionUpdate, Name: "anna", Password: "new-password", Role: store.RoleViewer}); got != "" {
		t.Fatal(got)
	}
	if _, err := st.SessionUser(ctx, res.Token); err == nil {
		t.Fatal("session kept after password change")
	}
	if got := call(t, login, LoginInput{Name: "anna", Password: "new-password"}); got != "" {
		t.Fatalf("login with new password: %q", got)
	}
	if got := call(t, admin, UserInput{Action: ActionDelete, Name: "anna"}); got != "" {
		t.Fatal(got)
	}
	if users, _ := st.Users(ctx); len(users) != 1 || users[0].Name != "carl" {
		t.Fatalf("users %+v", users)
	}
}

// Demoting and deleting two admins at once passes CheckUpdate for both while
// the first change still waits for its bcrypt hash; the store must keep one.
func TestConcurrentChangesKeepAdmin(t *testing.T) {
	st, _, admin := setup(t)
	ctx := context.Background()
	for _, name := range []string{"anna", "bert", "carl"} {
		if got := call(t, admin, UserInput{Action: ActionCreate, Name: name, Password: "0123456789", Role: store.RoleAdmin}); got != "" {
			t.Fatal(got)
		}
	}
	changes := []UserInput{
		{Action: ActionUpdate, Name: "anna", Password: "new-password", Role: store.RoleViewer},
		{Action: ActionUpdate, Name: "bert", Password: "new-password", Role: store.RoleViewer},
		{Action: ActionDelete, Name: "carl"},
	}
	got := make([]string, len(changes))
	var wg sync.WaitGroup
	for i, c := range changes {
		wg.Go(func() {
			outport, p, err := admin.Call(ctx, fbp.In, c)
			if err != nil {
				got[i] = err.Error()
			} else if outport == fbp.Error {
				got[i] = flow.ErrorMessage(p)
			}
		})
	}
	wg.Wait()
	if n, _ := st.CountAdmins(ctx); n != 1 {
		t.Fatalf("%d admins left, results %q", n, got)
	}
}
