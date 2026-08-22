package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	sessionPrefix = "pms_"
	tokenPrefix   = "pmt_"
	sessionHexN   = 32
	csrfBytes     = 32
	tokenHexN     = 32
	applyTimeout  = 5 * time.Second
)

type Principal struct {
	UserID, Username, SessionID, TokenID, CSRF string
}

type Clock func() time.Time

type Store interface {
	View() control.State
	Apply(cmd control.Command, timeout time.Duration) error
	HasQuorum() bool
	CacheFresh(ttl time.Duration) bool
}

type storeBox struct{ s Store }

type Service struct {
	store atomic.Value // storeBox
	Now   Clock        // nil → time.Now
	limit loginLimiter
}

func (s *Service) Store() Store {
	if s == nil {
		return nil
	}
	v := s.store.Load()
	if v == nil {
		return nil
	}
	return v.(storeBox).s
}

func (s *Service) SetStore(st Store) {
	if s == nil {
		return
	}
	s.store.Store(storeBox{s: st})
}

func (s *Service) storeOrErr() (Store, error) {
	st := s.Store()
	if st == nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "auth store not ready")
	}
	return st, nil
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func ValidPassword(pw string) error {
	if len(pw) < control.MinPasswordLen {
		return errcode.E(errcode.INVALID, "password too short")
	}
	return nil
}

func (s *Service) Login(username, password string) (sessionID, csrf, userID string, expires time.Time, err error) {
	if username == "" || password == "" {
		return "", "", "", time.Time{}, errcode.E(errcode.INVALID, "username and password required")
	}
	now := s.now()
	if !s.limit.allow(username, now) {
		return "", "", "", time.Time{}, errcode.E(errcode.RATE_LIMITED, "login rate limited")
	}

	stStore, err := s.storeOrErr()
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	st := stStore.View()
	u, ok := st.Users[username]
	if !ok || !control.VerifyPassword(u.PasswordHash, password) {
		if ok {
			_ = s.apply(control.CmdLoginFail, control.LoginFailBody{Username: username})
		}
		return "", "", "", time.Time{}, errcode.E(errcode.INVALID_CREDENTIALS, "invalid credentials")
	}
	if u.Status == control.UserDisabled {
		return "", "", "", time.Time{}, errcode.E(errcode.DENIED, "user disabled")
	}
	if u.Status == control.UserLocked && now.Before(time.Unix(u.LockedUntilUnix, 0)) {
		return "", "", "", time.Time{}, errcode.E(errcode.ACCOUNT_LOCKED, "user locked")
	}

	sid, err := randomPrefixed(sessionPrefix, sessionHexN)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	csrfHex, err := randomHex(csrfBytes)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	exp := now.Add(control.SessionTTL)
	if err := s.apply(control.CmdLoginOK, control.LoginOKBody{Username: username}); err != nil {
		return "", "", "", time.Time{}, err
	}
	if err := s.apply(control.CmdSessionPut, control.SessionPutBody{
		ID:          sid,
		UserID:      u.ID,
		CSRF:        csrfHex,
		ExpiresUnix: exp.Unix(),
	}); err != nil {
		return "", "", "", time.Time{}, err
	}
	return sid, csrfHex, u.ID, exp, nil
}

func (s *Service) Logout(sessionID string) error {
	return s.apply(control.CmdSessionDel, control.SessionDelBody{ID: sessionID})
}

func (s *Service) AuthenticateBearer(token string) (Principal, error) {
	switch {
	case strings.HasPrefix(token, sessionPrefix):
		return s.sessionPrincipal(token)
	case strings.HasPrefix(token, tokenPrefix):
		return s.tokenPrincipal(token)
	default:
		return Principal{}, errcode.E(errcode.DENIED, "invalid token")
	}
}

func (s *Service) AuthenticateSession(sessionID, csrf string, mutation bool) (Principal, error) {
	p, err := s.sessionPrincipal(sessionID)
	if err != nil {
		return Principal{}, err
	}
	if mutation && subtle.ConstantTimeCompare([]byte(csrf), []byte(p.CSRF)) != 1 {
		return Principal{}, errcode.E(errcode.DENIED, "csrf mismatch")
	}
	return p, nil
}

func (s *Service) CreateAPIToken(userID, name string, ttl time.Duration) (plain, tokenID string, expires time.Time, err error) {
	plain, err = randomPrefixed(tokenPrefix, tokenHexN)
	if err != nil {
		return "", "", time.Time{}, err
	}
	id, err := randomHex(16)
	if err != nil {
		return "", "", time.Time{}, err
	}
	var expUnix int64
	var exp time.Time
	if ttl > 0 {
		exp = s.now().Add(ttl)
		expUnix = exp.Unix()
	}
	if err := s.apply(control.CmdTokenPut, control.TokenPutBody{
		ID:          id,
		UserID:      userID,
		Name:        name,
		Hash:        hashToken(plain),
		ExpiresUnix: expUnix,
	}); err != nil {
		return "", "", time.Time{}, err
	}
	return plain, id, exp, nil
}

func (s *Service) RevokeAPIToken(tokenID string) error {
	return s.apply(control.CmdTokenRevoke, control.TokenRevokeBody{ID: tokenID})
}

func (s *Service) sessionPrincipal(sessionID string) (Principal, error) {
	stStore, err := s.storeOrErr()
	if err != nil {
		return Principal{}, err
	}
	st := stStore.View()
	sess, ok := st.SessionByID(sessionID)
	if !ok {
		return Principal{}, errcode.E(errcode.DENIED, "invalid session")
	}
	if expired(sess.ExpiresUnix, s.now()) {
		return Principal{}, errcode.E(errcode.DENIED, "session expired")
	}
	u, ok := userByID(st, sess.UserID)
	if !ok || u.Status != control.UserActive {
		return Principal{}, errcode.E(errcode.DENIED, "user not active")
	}
	return Principal{
		UserID:    u.ID,
		Username:  u.Username,
		SessionID: sess.ID,
		CSRF:      sess.CSRF,
	}, nil
}

func (s *Service) AuthenticateTokenID(tokenID string) (Principal, error) {
	if tokenID == "" {
		return Principal{}, errcode.E(errcode.DENIED, "invalid token")
	}
	stStore, err := s.storeOrErr()
	if err != nil {
		return Principal{}, err
	}
	st := stStore.View()
	tok, ok := st.APITokens[tokenID]
	if !ok {
		return Principal{}, errcode.E(errcode.DENIED, "invalid token")
	}
	return s.principalFromToken(st, tok)
}

func (s *Service) tokenPrincipal(plain string) (Principal, error) {
	stStore, err := s.storeOrErr()
	if err != nil {
		return Principal{}, err
	}
	st := stStore.View()
	tok, ok := st.TokenByPlain(plain)
	if !ok {
		return Principal{}, errcode.E(errcode.DENIED, "invalid token")
	}
	return s.principalFromToken(st, tok)
}

func (s *Service) principalFromToken(st control.State, tok control.APIToken) (Principal, error) {
	if tok.Revoked {
		return Principal{}, errcode.E(errcode.DENIED, "token revoked")
	}
	if expired(tok.ExpiresUnix, s.now()) {
		return Principal{}, errcode.E(errcode.DENIED, "token expired")
	}
	u, ok := userByID(st, tok.UserID)
	if !ok || u.Status != control.UserActive {
		return Principal{}, errcode.E(errcode.DENIED, "user not active")
	}
	return Principal{
		UserID:   u.ID,
		Username: u.Username,
		TokenID:  tok.ID,
	}, nil
}

func (s *Service) apply(typ string, body any) error {
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		return err
	}
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	return st.Apply(cmd, applyTimeout)
}

func userByID(st control.State, id string) (control.User, bool) {
	name, ok := st.UsersByID[id]
	if !ok {
		return control.User{}, false
	}
	u, ok := st.Users[name]
	return u, ok
}

// expired reports whether expiresUnix has passed. Zero means never (API tokens).
func expired(expiresUnix int64, now time.Time) bool {
	if expiresUnix == 0 {
		return false
	}
	return !now.Before(time.Unix(expiresUnix, 0))
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func randomPrefixed(prefix string, nBytes int) (string, error) {
	h, err := randomHex(nBytes)
	if err != nil {
		return "", err
	}
	return prefix + h, nil
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
