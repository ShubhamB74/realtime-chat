package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"realtime-chat/backend/internal/app/accounts"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
)

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int     `json:"expires_in"`
	RefreshToken string  `json:"refresh_token"`
	SessionID    string  `json:"session_id"`
	DeviceID     string  `json:"device_id"`
	User         userDTO `json:"user"`
}

func toTokenResponse(t accounts.Tokens) tokenResponse {
	return tokenResponse{
		AccessToken: t.AccessToken, TokenType: "Bearer", ExpiresIn: int(t.ExpiresIn.Seconds()),
		RefreshToken: t.RefreshToken, SessionID: t.SessionID, DeviceID: t.DeviceID, User: toUser(t.User),
	}
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, "register:ip:"+clientIP(r, s.TrustProxy), 10, time.Hour) {
		return
	}
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.Accounts.Register(r.Context(), accounts.RegisterInput{Email: in.Email, Password: in.Password, DisplayName: in.DisplayName})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUser(u)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, "login:ip:"+clientIP(r, s.TrustProxy), 30, 15*time.Minute) {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Device   struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Platform string `json:"platform"`
		} `json:"device"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.allow(w, r, "login:email:"+emailKey(in.Email), 10, 15*time.Minute) {
		return
	}
	devID := ""
	if in.Device.ID != "" {
		id, err := normalizeUUID(in.Device.ID)
		if err != nil {
			s.fail(w, r, domain.Invalid("device.id is not valid"))
			return
		}
		devID = id
	}
	tok, err := s.Accounts.Login(r.Context(), accounts.LoginInput{
		Email: in.Email, Password: in.Password,
		Device: accounts.DeviceInput{ID: devID, Name: in.Device.Name, Platform: in.Device.Platform},
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTokenResponse(tok))
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, "refresh:ip:"+clientIP(r, s.TrustProxy), 60, time.Minute) {
		return
	}
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	tok, err := s.Accounts.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTokenResponse(tok))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if err := s.Accounts.Logout(r.Context(), id.UserID, id.SessionID); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	u, err := s.Accounts.Me(r.Context(), id.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUser(u)})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		DisplayName *string `json:"display_name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.DisplayName == nil {
		s.fail(w, r, domain.Invalid("display_name is required"))
		return
	}
	u, err := s.Accounts.UpdateMe(r.Context(), id.UserID, *in.DisplayName)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUser(u)})
}

func (s *Server) lookupUser(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if !s.allow(w, r, "lookup:user:"+id.UserID, 30, time.Minute) {
		return
	}
	u, err := s.Accounts.LookupByEmail(r.Context(), r.URL.Query().Get("email"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUser(u)})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	devs, err := s.Accounts.ListDevices(r.Context(), id.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]deviceDTO, 0, len(devs))
	for _, d := range devs {
		out = append(out, deviceDTO{ID: d.ID, Name: d.Name, Platform: d.Platform, LastSeenAt: d.LastSeenAt, CreatedAt: d.CreatedAt, Current: d.ID == id.DeviceID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	devID, err := normalizeUUID(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, domain.NotFound())
		return
	}
	if err := s.Accounts.RevokeDevice(r.Context(), id.UserID, devID); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	sessions, err := s.Accounts.ListSessions(r.Context(), id.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]sessionDTO, 0, len(sessions))
	for _, x := range sessions {
		out = append(out, sessionDTO{
			ID: x.ID, DeviceID: x.DeviceID, DeviceName: x.DeviceName, Platform: x.Platform,
			StartedAt: x.StartedAt, LastRefreshedAt: x.LastRefreshedAt, ExpiresAt: x.ExpiresAt,
			Current: x.ID == id.SessionID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	sid, err := normalizeUUID(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, domain.NotFound())
		return
	}
	if err := s.Accounts.RevokeSession(r.Context(), id.UserID, sid); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, domain.Invalid("limit must be a positive integer")
	}
	return n, nil
}
