// Package users holds the Networks that change users and sessions of
// gofbpstore: login creates a session, password lets a user change its own
// password, useradmin creates, updates and deletes users. Checking a session
// on each request is a plain store query.
package users

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/pkg/fbp"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is the shortest password accepted.
const MinPasswordLength = 10

// SessionTTL is how long a session is valid.
const SessionTTL = 12 * time.Hour

// ErrLogin is the message of every failed login, so a caller cannot tell
// whether the name or the password was wrong.
var ErrLogin = errors.New("invalid name or password")

// ErrPassword is the message of a password change with a wrong current
// password.
var ErrPassword = errors.New("current password is wrong")

// Actions of the Network useradmin.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Outports of RouteAction.
const (
	Create = "Create"
	Update = "Update"
	Delete = "Delete"
)

// LoginInput is the Packet data of the Network login.
type LoginInput struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// LoginResult is the Success of the Network login.
type LoginResult struct {
	Token   string     `json:"token"`
	Expires time.Time  `json:"expires"`
	User    store.User `json:"user"`
}

// loginState travels between the Bricks of the Network login.
type loginState struct {
	LoginInput
	User store.User `json:"user"`
	Hash string     `json:"hash"`
}

// UserInput is the Packet data of the Network useradmin. Update sets Role
// and, unless empty, Password.
type UserInput struct {
	Action   string `json:"action"`
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	Role     string `json:"role,omitempty"`
	Hash     string `json:"hash,omitempty"` // set by HashPassword
}

// PasswordInput is the Packet data of the Network password: a user changes
// its own password. Name comes from the session, never from the browser.
type PasswordInput struct {
	Name        string `json:"name"`
	Password    string `json:"password"`
	NewPassword string `json:"new_password"`
}

// UserResult is the Success of the Network useradmin.
type UserResult struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
}

// dummyHash is compared when a user does not exist, so a failed login
// takes as long for an unknown name as for a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not a password"), bcrypt.DefaultCost)

// FindUser looks up the user of a LoginInput.
func FindUser(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "FindUser",
		Description: "Looks up the user of a login",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[LoginInput](p)
			if err != nil {
				return err
			}
			u, err := st.User(ctx, in.Name)
			if errors.Is(err, store.ErrNotFound) {
				bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
				return ErrLogin
			}
			if err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, loginState{LoginInput: in, User: u, Hash: u.PwHash})
		},
	}
}

// CheckPassword compares the password of a login with the user's hash.
func CheckPassword() *fbp.Brick {
	return &fbp.Brick{
		Name:        "CheckPassword",
		Description: "Compares the password of a login with the stored hash",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[loginState](p)
			if err != nil {
				return err
			}
			if bcrypt.CompareHashAndPassword([]byte(in.Hash), []byte(in.Password)) != nil {
				return ErrLogin
			}
			// Neither password nor hash travel further.
			return fbp.EmitAs(ctx, out, fbp.Success, loginState{User: in.User})
		},
	}
}

// CreateSession stores a new session for the user and emits a LoginResult
// with its token.
func CreateSession(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "CreateSession",
		Description: "Creates a session for a logged-in user",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[loginState](p)
			if err != nil {
				return err
			}
			var b [32]byte
			rand.Read(b[:])
			token := hex.EncodeToString(b[:])
			expires := time.Now().Add(SessionTTL)
			if err := st.InsertSession(ctx, token, in.User.ID, expires); err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, LoginResult{Token: token, Expires: expires, User: in.User})
		},
	}
}

// RouteAction sends a UserInput to the Outport of its action.
func RouteAction() *fbp.Brick {
	return &fbp.Brick{
		Name:        "RouteAction",
		Description: "Routes a user change by its action",
		Outports:    []string{Create, Update, Delete},
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			switch in.Action {
			case ActionCreate:
				return out.Emit(ctx, Create, p.Data)
			case ActionUpdate:
				return out.Emit(ctx, Update, p.Data)
			case ActionDelete:
				return out.Emit(ctx, Delete, p.Data)
			}
			return fmt.Errorf("unknown action %q", in.Action)
		},
	}
}

func checkRole(role string) error {
	if role != store.RoleAdmin && role != store.RoleViewer {
		return fmt.Errorf("role must be %s or %s", store.RoleAdmin, store.RoleViewer)
	}
	return nil
}

func checkPassword(pw string) error {
	if len(pw) < MinPasswordLength {
		return fmt.Errorf("password must have at least %d characters", MinPasswordLength)
	}
	return nil
}

// CheckNewUser checks name, role and password of a user to create.
func CheckNewUser() *fbp.Brick {
	return &fbp.Brick{
		Name:        "CheckNewUser",
		Description: "Checks name, role and password of a new user",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if strings.TrimSpace(in.Name) == "" || in.Name != strings.TrimSpace(in.Name) {
				return errors.New("name must not be empty or start or end with white space")
			}
			if err := checkRole(in.Role); err != nil {
				return err
			}
			if err := checkPassword(in.Password); err != nil {
				return err
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// keepsAdmin returns an error if changing name to role (or deleting it,
// role "") would leave no admin. It only fails early, before the bcrypt
// hash: concurrent changes can still pass it together, so the store checks
// again atomically with the write (store.ErrLastAdmin).
func keepsAdmin(ctx context.Context, st *store.Store, name, role string) error {
	u, err := st.User(ctx, name)
	if err != nil {
		return err
	}
	if u.Role != store.RoleAdmin || role == store.RoleAdmin {
		return nil
	}
	n, err := st.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return store.ErrLastAdmin
	}
	return nil
}

// CheckUpdate checks the new role and password of a user and that the last
// admin stays admin.
func CheckUpdate(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "CheckUpdate",
		Description: "Checks a role or password change; the last admin stays admin",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if err := checkRole(in.Role); err != nil {
				return err
			}
			if in.Password != "" {
				if err := checkPassword(in.Password); err != nil {
					return err
				}
			}
			if err := keepsAdmin(ctx, st, in.Name, in.Role); err != nil {
				return err
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// HashPassword replaces the password of a UserInput by its bcrypt hash; an
// empty password stays empty.
func HashPassword() *fbp.Brick {
	return &fbp.Brick{
		Name:        "HashPassword",
		Description: "Replaces the password by its bcrypt hash",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if in.Password != "" {
				h, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
				if err != nil {
					return err
				}
				in.Hash = string(h)
			}
			in.Password = ""
			return fbp.EmitAs(ctx, out, fbp.Success, in)
		},
	}
}

// InsertUser creates the user of a hashed UserInput.
func InsertUser(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "InsertUser",
		Description: "Creates a user",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if _, err := st.User(ctx, in.Name); err == nil {
				return fmt.Errorf("user %q exists", in.Name)
			}
			if err := st.InsertUser(ctx, in.Name, in.Hash, in.Role); err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, UserResult{Action: in.Action, Name: in.Name, Role: in.Role})
		},
	}
}

// UpdateUser sets role and, if hashed, password of a user.
func UpdateUser(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "UpdateUser",
		Description: "Sets role and password of a user",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if err := st.UpdateUser(ctx, in.Name, in.Hash, in.Role); err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, UserResult{Action: in.Action, Name: in.Name, Role: in.Role})
		},
	}
}

// CheckNewPassword checks the new password of a PasswordInput.
func CheckNewPassword() *fbp.Brick {
	return &fbp.Brick{
		Name:        "CheckNewPassword",
		Description: "Checks the new password of a password change",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[PasswordInput](p)
			if err != nil {
				return err
			}
			if err := checkPassword(in.NewPassword); err != nil {
				return err
			}
			if in.NewPassword == in.Password {
				return errors.New("new password must differ from the current one")
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// VerifyPassword compares the current password of a PasswordInput with the
// user's hash and emits a UserInput with the new password for HashPassword.
func VerifyPassword(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "VerifyPassword",
		Description: "Compares the current password of a password change with the stored hash",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[PasswordInput](p)
			if err != nil {
				return err
			}
			u, err := st.User(ctx, in.Name)
			if err != nil {
				return err
			}
			if bcrypt.CompareHashAndPassword([]byte(u.PwHash), []byte(in.Password)) != nil {
				return ErrPassword
			}
			return fbp.EmitAs(ctx, out, fbp.Success, UserInput{Name: in.Name, Password: in.NewPassword})
		},
	}
}

// SetPassword stores the hash of a hashed UserInput, which ends all sessions
// of the user, and emits the user for CreateSession.
func SetPassword(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "SetPassword",
		Description: "Sets the password of a user and ends its sessions",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if in.Hash == "" {
				return errors.New("no password hash")
			}
			if err := st.SetPassword(ctx, in.Name, in.Hash); err != nil {
				return err
			}
			u, err := st.User(ctx, in.Name)
			if err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, loginState{User: u})
		},
	}
}

// DeleteUser deletes a user and its sessions; the last admin is kept.
func DeleteUser(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "DeleteUser",
		Description: "Deletes a user; the last admin is kept",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[UserInput](p)
			if err != nil {
				return err
			}
			if err := keepsAdmin(ctx, st, in.Name, ""); err != nil {
				return err
			}
			if err := st.DeleteUser(ctx, in.Name); err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, UserResult{Action: in.Action, Name: in.Name})
		},
	}
}
