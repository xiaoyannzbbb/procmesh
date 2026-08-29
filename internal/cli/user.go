package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runUser(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return userList(c, stdout)
	case "create":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return userCreate(c, opt, stdout)
	case "disable":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("user disable requires USER_ID")
		}
		return userDisable(c, pos[0], stdout)
	case "enable":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("user enable requires USER_ID")
		}
		return userEnable(c, pos[0], stdout)
	default:
		return usageError("unknown user command")
	}
}

func userList(c *client, stdout io.Writer) error {
	resp, err := c.user.ListUsers(context.Background(), connect.NewRequest(&procmeshv1.ListUsersRequest{}))
	if err != nil {
		return err
	}
	for _, u := range resp.Msg.GetUsers() {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n",
			u.GetUserId(), u.GetUsername(), u.GetStatus(), u.GetDisplayName(), u.GetEmail())
	}
	return nil
}

func userCreate(c *client, opt options, stdout io.Writer) error {
	if opt.user == "" || opt.password == "" {
		return usageError("user create requires --user and --password")
	}
	resp, err := c.user.CreateUser(context.Background(), connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:        c.meta(),
		Username:    opt.user,
		Password:    opt.password,
		DisplayName: opt.display,
		Email:       opt.email,
	}))
	if err != nil {
		return err
	}
	u := resp.Msg.GetUser()
	fmt.Fprintf(stdout, "user_id=%s\n", u.GetUserId())
	fmt.Fprintf(stdout, "username=%s\n", u.GetUsername())
	fmt.Fprintf(stdout, "status=%s\n", u.GetStatus())
	return nil
}

func userDisable(c *client, userID string, stdout io.Writer) error {
	resp, err := c.user.DisableUser(context.Background(), connect.NewRequest(&procmeshv1.DisableUserRequest{
		Meta:   c.meta(),
		UserId: userID,
	}))
	if err != nil {
		return err
	}
	u := resp.Msg.GetUser()
	fmt.Fprintf(stdout, "user_id=%s\n", u.GetUserId())
	fmt.Fprintf(stdout, "status=%s\n", u.GetStatus())
	return nil
}

func userEnable(c *client, userID string, stdout io.Writer) error {
	resp, err := c.user.EnableUser(context.Background(), connect.NewRequest(&procmeshv1.EnableUserRequest{
		Meta:   c.meta(),
		UserId: userID,
	}))
	if err != nil {
		return err
	}
	u := resp.Msg.GetUser()
	fmt.Fprintf(stdout, "user_id=%s\n", u.GetUserId())
	fmt.Fprintf(stdout, "status=%s\n", u.GetStatus())
	return nil
}

func runRole(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return roleList(c, stdout)
	case "create":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return roleCreate(c, opt, stdout)
	case "grant":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return roleGrant(c, opt, stdout)
	default:
		return usageError("unknown role command")
	}
}

func roleList(c *client, stdout io.Writer) error {
	resp, err := c.role.ListRoles(context.Background(), connect.NewRequest(&procmeshv1.ListRolesRequest{}))
	if err != nil {
		return err
	}
	for _, r := range resp.Msg.GetRoles() {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", r.GetRoleId(), r.GetName(), strings.Join(r.GetPermissions(), ","))
	}
	for _, b := range resp.Msg.GetBindings() {
		fmt.Fprintf(stdout, "binding\t%s\t%s\t%s\t%s\n",
			b.GetUserId(), b.GetRoleId(), b.GetScopeType(), b.GetScopeId())
	}
	return nil
}

func roleCreate(c *client, opt options, stdout io.Writer) error {
	if opt.name == "" || len(opt.perms) == 0 {
		return usageError("role create requires --name and --perm")
	}
	resp, err := c.role.CreateRole(context.Background(), connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        c.meta(),
		Name:        opt.name,
		Permissions: opt.perms,
	}))
	if err != nil {
		return err
	}
	r := resp.Msg.GetRole()
	fmt.Fprintf(stdout, "role_id=%s\n", r.GetRoleId())
	fmt.Fprintf(stdout, "name=%s\n", r.GetName())
	return nil
}

func roleGrant(c *client, opt options, stdout io.Writer) error {
	if opt.userID == "" || opt.roleID == "" {
		return usageError("role grant requires --user-id and --role-id")
	}
	scope := opt.scope
	if scope == "" {
		scope = "CLUSTER"
	}
	resp, err := c.role.GrantRole(context.Background(), connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      c.meta(),
		UserId:    opt.userID,
		RoleId:    opt.roleID,
		ScopeType: scope,
		ScopeId:   opt.scopeID,
	}))
	if err != nil {
		return err
	}
	b := resp.Msg.GetBinding()
	fmt.Fprintf(stdout, "user_id=%s\n", b.GetUserId())
	fmt.Fprintf(stdout, "role_id=%s\n", b.GetRoleId())
	fmt.Fprintf(stdout, "scope=%s\n", b.GetScopeType())
	fmt.Fprintf(stdout, "scope_id=%s\n", b.GetScopeId())
	return nil
}
