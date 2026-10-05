// SPDX-License-Identifier: Apache-2.0

package apply_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/apply"
)

// inviteConfig carries the bootstrap section plus an identityd
// placement whose env_file holds the bootstrap key.
const inviteConfig = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "` + stateEnvFile + `", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts:
  - id: gw-1
    address: 10.0.0.11
  - id: app-1
    address: 10.0.0.12
placements:
  - {component: gateway-front, host: gw-1, ports: {http: 8080}}
  - {component: identityd, host: app-1, ports: {http: 8081}, env_file: "/etc/looming/identityd.env"}
bootstrap: {admin_email: "admin@example.com"}
`

const identitydEnvFile = "/etc/looming/identityd.env"

const identitydEnvFileContent = "IDENTITY_LISTEN=:8081\nIDENTITY_BOOTSTRAP_KEY=topsecret\n"

// firstBootAndSecondRunScripts scripts a two-apply sequence: full
// first boot, then an everything-unchanged re-apply.
func firstBootAndSecondRunScripts() []scriptedCall {
	return append(pgUpScript(),
		scriptedCall{}, // run 1: ensure gw-1
		scriptedCall{}, // run 1: ensure app-1
		scriptedCall{stdout: "looming-bundle-postgres-1\n"}, // run 2: ps — running
		scriptedCall{}, // run 2: pg_isready ok
	)
}

func inviteWorld(t *testing.T, files map[string]string) *world {
	t.Helper()
	if files == nil {
		files = map[string]string{identitydEnvFile: identitydEnvFileContent}
	}
	files[stateEnvFile] = stateEnvFileContent
	return newWorld(t, firstBootAndSecondRunScripts(), files)
}

func TestApplyPrintsBootstrapInviteAfterChangedConverge(t *testing.T) {
	w := inviteWorld(t, nil)
	path := writeConfig(t, inviteConfig)

	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Printed)
	assert.False(t, res.Invite.Skipped)
	assert.False(t, res.Invite.Warning)
	assert.Equal(t, "invite-token-1", res.Invite.Token)
	assert.Equal(t, "http://10.0.0.12:8081", res.Invite.Endpoint)
	assert.Equal(t, "/v1/self/register", res.Invite.RegisterPath)
	assert.Equal(t, "admin@example.com", res.Invite.AdminEmail)

	require.Len(t, w.inviteCalls, 1)
	assert.Equal(t, "http://10.0.0.12:8081", w.inviteCalls[0].endpoint)
	assert.Equal(t, "topsecret", w.inviteCalls[0].key, "the key came from the identityd placement's env_file")
	assert.Equal(t, "admin@example.com", w.inviteCalls[0].email)
}

func TestApplySkipsInviteWhenNothingChangedWithoutForce(t *testing.T) {
	w := inviteWorld(t, nil)
	path := writeConfig(t, inviteConfig)

	first, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	require.True(t, first.Invite.Printed)

	second, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	require.NotNil(t, second.Invite)
	assert.True(t, second.Invite.Skipped)
	assert.False(t, second.Invite.Attempted, "no request goes out on an unchanged converge")
	assert.Contains(t, second.Invite.Reason, "--print-invite")
	assert.Len(t, w.inviteCalls, 1, "exactly one request across both runs")
}

func TestApplyPrintInviteForcesTheRequest(t *testing.T) {
	w := inviteWorld(t, nil)
	path := writeConfig(t, inviteConfig)

	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)

	second, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path, PrintInvite: true})
	require.NoError(t, err)
	require.NotNil(t, second.Invite)
	assert.True(t, second.Invite.Attempted)
	assert.True(t, second.Invite.Printed)
	assert.Len(t, w.inviteCalls, 2)
}

func TestApplyInviteConflictSkips(t *testing.T) {
	w := inviteWorld(t, nil)
	w.inviteFn = func(_, _, _ string) (int, []byte, error) {
		return http.StatusConflict, []byte(`{"error":{"code":"bootstrap_closed","message":"bootstrap_closed"}}`), nil
	}
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Skipped)
	assert.Contains(t, res.Invite.Reason, "already created or an admin exists")
	assert.True(t, res.Invite.Attempted)
}

func TestApplyInviteAuthFailureWarnsAndContinues(t *testing.T) {
	w := inviteWorld(t, nil)
	w.inviteFn = func(_, _, _ string) (int, []byte, error) {
		return http.StatusUnauthorized, []byte(`{"error":{"code":"unauthenticated","message":"unauthenticated"}}`), nil
	}
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err, "a 401 never fails the converge")
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "401")
	assert.False(t, res.Failed())
}

func TestApplyInviteServerErrorWarnsAndContinues(t *testing.T) {
	w := inviteWorld(t, nil)
	w.inviteFn = func(_, _, _ string) (int, []byte, error) {
		return http.StatusInternalServerError, []byte(`{"error":{"code":"internal","message":"internal"}}`), nil
	}
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "500")
	assert.False(t, res.Failed())
}

func TestApplyInviteRequestErrorWarnsAndContinues(t *testing.T) {
	w := inviteWorld(t, nil)
	w.inviteFn = func(_, _, _ string) (int, []byte, error) {
		return 0, nil, errors.New("connection refused")
	}
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "connection refused")
	assert.False(t, res.Failed())
}

func TestApplyInviteMalformed201BodyWarns(t *testing.T) {
	w := inviteWorld(t, nil)
	w.inviteFn = func(_, _, _ string) (int, []byte, error) {
		return http.StatusCreated, []byte(`{"nope":true}`), nil
	}
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "201")
}

func TestApplyNoBootstrapSectionLeavesInviteNil(t *testing.T) {
	w := newWorld(t, firstBootAndSecondRunScripts(), map[string]string{stateEnvFile: stateEnvFileContent})
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	assert.Nil(t, res.Invite)
	assert.Empty(t, w.inviteCalls)
}

func TestApplyInviteWithoutIdentitydPlacementWarns(t *testing.T) {
	w := inviteWorld(t, nil)
	noIdentityd := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "` + stateEnvFile + `", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts: [{id: gw-1, address: 10.0.0.11}]
placements: [{component: gateway-front, host: gw-1, ports: {http: 8080}}]
bootstrap: {admin_email: "admin@example.com"}
`
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, noIdentityd)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "no identityd placement")
	assert.Empty(t, w.inviteCalls)
}

func TestApplyInviteWithoutEnvFileWarns(t *testing.T) {
	w := inviteWorld(t, nil)
	noEnvFile := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "` + stateEnvFile + `", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}}
  - {component: identityd, host: only, ports: {http: 8081}}
bootstrap: {admin_email: "admin@example.com"}
`
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, noEnvFile)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "env_file")
	assert.Empty(t, w.inviteCalls)
}

func TestApplyInviteMissingKeyInEnvFileWarns(t *testing.T) {
	w := inviteWorld(t, map[string]string{identitydEnvFile: "IDENTITY_LISTEN=:8081\n"})
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "IDENTITY_BOOTSTRAP_KEY")
	assert.Empty(t, w.inviteCalls)
}

func TestApplyInviteUnreadableEnvFileWarns(t *testing.T) {
	w := inviteWorld(t, map[string]string{}) // identityd env_file absent from the fake fs
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, inviteConfig)})
	require.NoError(t, err)
	require.NotNil(t, res.Invite)
	assert.True(t, res.Invite.Warning)
	assert.Contains(t, res.Invite.Reason, "read identityd env_file")
	assert.Empty(t, w.inviteCalls)
}
