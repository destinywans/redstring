package v3admin

type role struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Members int    `json:"members"`
	Kind    string `json:"kind"`
}

type matrixRow struct {
	Resource string `json:"resource"`
	Grants   []int  `json:"grants"`
}

type roleDetail struct {
	role
	Desc        string      `json:"desc"`
	MemberNames []string    `json:"member_names"`
	Scope       []string    `json:"scope"`
	Actions     []string    `json:"actions"`
	Matrix      []matrixRow `json:"matrix"`
	// EnvironmentAccess lists the role's environment.view scopes ("global"
	// or environment IDs) visible in the selected environments. It is not a
	// matrix row; without it the role's members see no environment (PA-55).
	EnvironmentAccess []string `json:"environment_access"`
	Notes             string   `json:"notes"`
}

type orgNode struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	Depth int    `json:"depth"`
	Kind  string `json:"kind"`
}

type member struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Email      string   `json:"email"`
	Department string   `json:"department"`
	Roles      []string `json:"roles"`
	Phone      string   `json:"phone"`
	MFA        string   `json:"mfa"`
	Source     string   `json:"source"`
	Status     string   `json:"status"`
	LastLogin  *string  `json:"last_login"`
	Agent      bool     `json:"agent,omitempty"`
}

type memberRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// memberDetail exposes only fields the account and association stores can
// prove. External sync, MFA, and login evidence remain explicit unknowns.
type memberDetail struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	Email              string      `json:"email"`
	Department         *memberRef  `json:"department"`
	Roles              []memberRef `json:"roles"`
	Phone              *string     `json:"phone"`
	Status             string      `json:"status"`
	IdentityKind       string      `json:"identity_kind"`
	RecordedSource     string      `json:"recorded_source"`
	MFA                string      `json:"mfa"`
	LastLogin          *string     `json:"last_login"`
	DirectorySyncState string      `json:"directory_sync_state"`
	Revision           string      `json:"revision"`
}

type memberCounts struct {
	All      int  `json:"all"`
	Admin    int  `json:"admin"`
	NoMFA    *int `json:"no_mfa"`
	Disabled int  `json:"disabled"`
}

type membersView struct {
	Org               []orgNode    `json:"org"`
	Groups            []orgNode    `json:"groups"`
	Items             []member     `json:"items"`
	Counts            memberCounts `json:"counts"`
	MFAGraceDays      *int         `json:"mfa_grace_days"`
	CompleteV3DTO     bool         `json:"complete_v3_dto"`
	UnavailableFields []string     `json:"unavailable_fields"`
}
