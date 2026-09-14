package api

import "testing"

func TestSessionIsAuthenticated(t *testing.T) {
	tests := []struct {
		name    string
		session *SessionResponse
		want    bool
	}{
		{name: "nil session", session: nil, want: false},
		{name: "empty user", session: &SessionResponse{}, want: false},
		{name: "user with id", session: &SessionResponse{User: SessionUser{ID: "u-1"}}, want: true},
		{name: "user with email only", session: &SessionResponse{User: SessionUser{Email: "a@b.com"}}, want: true},
		{name: "user with id and email", session: &SessionResponse{User: SessionUser{ID: "u-1", Email: "a@b.com"}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionIsAuthenticated(tt.session); got != tt.want {
				t.Errorf("SessionIsAuthenticated() = %t, want %t", got, tt.want)
			}
		})
	}
}
