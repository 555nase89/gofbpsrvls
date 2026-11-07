package users

import (
	"errors"

	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/pkg/fbp"
)

// Login checks the name and password of a LoginInput and creates a session;
// Success carries a LoginResult. Every failure leaves through Error, a wrong
// name or password with the message of ErrLogin.
func Login(st *store.Store) (*fbp.Network, error) {
	n := &fbp.Network{Name: "login", Description: "checks name and password and creates a session"}
	return n, errors.Join(
		n.Add("finduser", FindUser(st)),
		n.Add("checkpassword", CheckPassword()),
		n.Add("createsession", CreateSession(st)),
		n.ConnectIn("finduser"),
		n.Connect("finduser", fbp.Success, "checkpassword"),
		n.Connect("checkpassword", fbp.Success, "createsession"),
		n.ConnectOut("createsession", fbp.Success, fbp.Success),
		n.ConnectOut("finduser", fbp.Error, fbp.Error),
		n.ConnectOut("checkpassword", fbp.Error, fbp.Error),
		n.ConnectOut("createsession", fbp.Error, fbp.Error),
	)
}

// Useradmin creates, updates or deletes the user of a UserInput, by its
// action; Success carries a UserResult.
func Useradmin(st *store.Store) (*fbp.Network, error) {
	n := &fbp.Network{Name: "useradmin", Description: "creates, updates and deletes users"}
	return n, errors.Join(
		n.Add("routeaction", RouteAction()),
		n.Add("checknewuser", CheckNewUser()),
		n.Add("hashnew", HashPassword()),
		n.Add("insertuser", InsertUser(st)),
		n.Add("checkupdate", CheckUpdate(st)),
		n.Add("hashupdate", HashPassword()),
		n.Add("updateuser", UpdateUser(st)),
		n.Add("deleteuser", DeleteUser(st)),
		n.ConnectIn("routeaction"),
		n.Connect("routeaction", Create, "checknewuser"),
		n.Connect("checknewuser", fbp.Success, "hashnew"),
		n.Connect("hashnew", fbp.Success, "insertuser"),
		n.Connect("routeaction", Update, "checkupdate"),
		n.Connect("checkupdate", fbp.Success, "hashupdate"),
		n.Connect("hashupdate", fbp.Success, "updateuser"),
		n.Connect("routeaction", Delete, "deleteuser"),
		n.ConnectOut("insertuser", fbp.Success, fbp.Success),
		n.ConnectOut("updateuser", fbp.Success, fbp.Success),
		n.ConnectOut("deleteuser", fbp.Success, fbp.Success),
		n.ConnectOut("routeaction", fbp.Error, fbp.Error),
		n.ConnectOut("checknewuser", fbp.Error, fbp.Error),
		n.ConnectOut("hashnew", fbp.Error, fbp.Error),
		n.ConnectOut("insertuser", fbp.Error, fbp.Error),
		n.ConnectOut("checkupdate", fbp.Error, fbp.Error),
		n.ConnectOut("hashupdate", fbp.Error, fbp.Error),
		n.ConnectOut("updateuser", fbp.Error, fbp.Error),
		n.ConnectOut("deleteuser", fbp.Error, fbp.Error),
	)
}

// Password changes the password of the user of a PasswordInput after checking
// its current one, ends all its sessions and creates a new one; Success
// carries a LoginResult. A wrong current password leaves through Error with
// the message of ErrPassword.
func Password(st *store.Store) (*fbp.Network, error) {
	n := &fbp.Network{Name: "password", Description: "changes the own password and creates a new session"}
	return n, errors.Join(
		n.Add("checknewpassword", CheckNewPassword()),
		n.Add("verifypassword", VerifyPassword(st)),
		n.Add("hashpassword", HashPassword()),
		n.Add("setpassword", SetPassword(st)),
		n.Add("createsession", CreateSession(st)),
		n.ConnectIn("checknewpassword"),
		n.Connect("checknewpassword", fbp.Success, "verifypassword"),
		n.Connect("verifypassword", fbp.Success, "hashpassword"),
		n.Connect("hashpassword", fbp.Success, "setpassword"),
		n.Connect("setpassword", fbp.Success, "createsession"),
		n.ConnectOut("createsession", fbp.Success, fbp.Success),
		n.ConnectOut("checknewpassword", fbp.Error, fbp.Error),
		n.ConnectOut("verifypassword", fbp.Error, fbp.Error),
		n.ConnectOut("hashpassword", fbp.Error, fbp.Error),
		n.ConnectOut("setpassword", fbp.Error, fbp.Error),
		n.ConnectOut("createsession", fbp.Error, fbp.Error),
	)
}
