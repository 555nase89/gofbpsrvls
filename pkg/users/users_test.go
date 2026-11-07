package users

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"gofbpsrvls/pkg/flow"
	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/fbptest"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// starter returns a function starting a Network built by Login or Useradmin.
func starter(t *testing.T) func(*fbp.Network, error) *fbptest.Instance {
	return func(net *fbp.Network, err error) *fbptest.Instance {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return fbptest.Start(t, net, fbp.WithLogger(slog.New(slog.DiscardHandler)))
	}
}

// js returns v as JSON text.
func js(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRouteAction(t *testing.T) {
	for action, outport := range map[string]string{ActionCreate: Create, ActionUpdate: Update, ActionDelete: Delete} {
		in := js(t, UserInput{Action: action, Name: "anna"})
		fbptest.CallBrick(t, RouteAction(), in).Want(t, fbptest.Outputs{outport: {in}})
	}
	fbptest.CallBrick(t, RouteAction(), js(t, UserInput{Action: "rename"})).WantError(t, "", `unknown action "rename"`)
}

func TestCheckNewUser(t *testing.T) {
	ok := UserInput{Action: ActionCreate, Name: "bob", Password: "0123456789", Role: store.RoleViewer}
	fbptest.CallBrick(t, CheckNewUser(), js(t, ok)).Want(t, fbptest.Outputs{fbp.Success: {js(t, ok)}})
	for _, c := range []struct {
		in   UserInput
		want string
	}{
		{UserInput{Name: "bob", Password: "short", Role: store.RoleViewer}, "password must have at least 10 characters"},
		{UserInput{Name: " bob", Password: "0123456789", Role: store.RoleViewer}, "name must not be empty or start or end with white space"},
		{UserInput{Name: "", Password: "0123456789", Role: store.RoleViewer}, "name must not be empty or start or end with white space"},
		{UserInput{Name: "bob", Password: "0123456789", Role: "root"}, "role must be admin or viewer"},
	} {
		fbptest.CallBrick(t, CheckNewUser(), js(t, c.in)).WantError(t, "", c.want)
	}
}

func TestUseradminAndLogin(t *testing.T) {
	st := openStore(t)
	start := starter(t)
	login, admin := start(Login(st)), start(Useradmin(st))
	ctx := context.Background()
	// changed runs in through admin and wants the UserResult of in on Success.
	changed := func(in UserInput) {
		t.Helper()
		want := UserResult{Action: in.Action, Name: in.Name, Role: in.Role}
		admin.Run(t, js(t, in)).Want(t, fbptest.Outputs{fbp.Success: {js(t, want)}})
	}

	anna := UserInput{Action: ActionCreate, Name: "anna", Password: "0123456789", Role: store.RoleAdmin}
	changed(anna)
	admin.Run(t, js(t, anna)).WantError(t, "insertuser", `user "anna" exists`)
	admin.Run(t, js(t, UserInput{Action: ActionCreate, Name: "bob", Password: "short", Role: store.RoleViewer})).
		WantError(t, "checknewuser", "password must have at least 10 characters")
	admin.Run(t, js(t, UserInput{Action: "rename", Name: "bob"})).WantError(t, "routeaction", `unknown action "rename"`)
	admin.Run(t, js(t, UserInput{Action: ActionUpdate, Name: "anna", Role: store.RoleViewer})).
		WantError(t, "checkupdate", store.ErrLastAdmin.Error())
	admin.Run(t, js(t, UserInput{Action: ActionDelete, Name: "anna"})).
		WantError(t, "deleteuser", store.ErrLastAdmin.Error())

	// A wrong password and an unknown name fail with the same message.
	login.Run(t, js(t, LoginInput{Name: "anna", Password: "wrong-password"})).WantError(t, "", ErrLogin.Error())
	login.Run(t, js(t, LoginInput{Name: "nobody", Password: "0123456789"})).WantError(t, "", ErrLogin.Error())
	token := loginToken(t, login, LoginInput{Name: "anna", Password: "0123456789"}, store.RoleAdmin)
	if u, err := st.SessionUser(ctx, token); err != nil || u.Name != "anna" {
		t.Fatalf("session: %+v %v", u, err)
	}

	// A second admin makes demoting and deleting the first possible; the
	// new password ends anna's session.
	changed(UserInput{Action: ActionCreate, Name: "carl", Password: "abcdefghij", Role: store.RoleAdmin})
	changed(UserInput{Action: ActionUpdate, Name: "anna", Password: "new-password", Role: store.RoleViewer})
	if _, err := st.SessionUser(ctx, token); err == nil {
		t.Fatal("session kept after password change")
	}
	loginToken(t, login, LoginInput{Name: "anna", Password: "new-password"}, store.RoleViewer)
	changed(UserInput{Action: ActionDelete, Name: "anna"})
	if users, _ := st.Users(ctx); len(users) != 1 || users[0].Name != "carl" {
		t.Fatalf("users %+v", users)
	}
}

func TestPassword(t *testing.T) {
	st := openStore(t)
	start := starter(t)
	login, admin, password := start(Login(st)), start(Useradmin(st)), start(Password(st))
	ctx := context.Background()
	in := UserInput{Action: ActionCreate, Name: "vera", Password: "0123456789", Role: store.RoleViewer}
	admin.Run(t, js(t, in)).Want(t, fbptest.Outputs{fbp.Success: {js(t, UserResult{Action: in.Action, Name: in.Name, Role: in.Role})}})
	old := loginToken(t, login, LoginInput{Name: "vera", Password: "0123456789"}, store.RoleViewer)

	for _, c := range []struct {
		in         PasswordInput
		node, want string
	}{
		{PasswordInput{Name: "vera", Password: "0123456789", NewPassword: "short"}, "checknewpassword", "password must have at least 10 characters"},
		{PasswordInput{Name: "vera", Password: "0123456789", NewPassword: "0123456789"}, "checknewpassword", "new password must differ from the current one"},
		{PasswordInput{Name: "vera", Password: "wrong-password", NewPassword: "new-password"}, "verifypassword", ErrPassword.Error()},
	} {
		password.Run(t, js(t, c.in)).WantError(t, c.node, c.want)
	}

	res := password.Run(t, js(t, PasswordInput{Name: "vera", Password: "0123456789", NewPassword: "new-password"}))
	out := res.Outputs[fbp.Success]
	if len(out) != 1 || len(res.Outputs) != 1 {
		t.Fatalf("password: got %s", res)
	}
	lr, err := fbp.DataAs[LoginResult](fbp.Packet{Data: out[0]})
	if err != nil || lr.Token == "" || lr.User.Name != "vera" || lr.User.Role != store.RoleViewer {
		t.Fatalf("password: got %+v, %v", lr, err)
	}
	// The old session ends, the new one is valid, and only the new password logs in.
	if _, err := st.SessionUser(ctx, old); err == nil {
		t.Fatal("old session kept after password change")
	}
	if u, err := st.SessionUser(ctx, lr.Token); err != nil || u.Name != "vera" {
		t.Fatalf("new session: %+v %v", u, err)
	}
	login.Run(t, js(t, LoginInput{Name: "vera", Password: "0123456789"})).WantError(t, "", ErrLogin.Error())
	loginToken(t, login, LoginInput{Name: "vera", Password: "new-password"}, store.RoleViewer)
}

// loginToken runs in through login, wants exactly one LoginResult with role
// on Success and returns its token.
func loginToken(t *testing.T, login *fbptest.Instance, in LoginInput, role string) string {
	t.Helper()
	res := login.Run(t, js(t, in))
	out := res.Outputs[fbp.Success]
	if len(out) != 1 || len(res.Outputs) != 1 {
		t.Fatalf("login %s: got %s", in.Name, res)
	}
	lr, err := fbp.DataAs[LoginResult](fbp.Packet{Data: out[0]})
	if err != nil || lr.Token == "" || lr.User.Name != in.Name || lr.User.Role != role {
		t.Fatalf("login %s: got %+v, %v", in.Name, lr, err)
	}
	return lr.Token
}

// Demoting and deleting two admins at once passes CheckUpdate for both while
// the first change still waits for its bcrypt hash; the store must keep one.
func TestConcurrentChangesKeepAdmin(t *testing.T) {
	st := openStore(t)
	net, err := Useradmin(st)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := flow.Start(context.Background(), net, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Stop)
	ctx := context.Background()
	for _, name := range []string{"anna", "bert", "carl"} {
		if outport, p, err := admin.Call(ctx, fbp.In, UserInput{Action: ActionCreate, Name: name, Password: "0123456789", Role: store.RoleAdmin}); err != nil || outport != fbp.Success {
			t.Fatalf("create %s: %s %s %v", name, outport, p.Data, err)
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
