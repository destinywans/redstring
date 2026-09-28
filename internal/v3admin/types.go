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
	Notes       string      `json:"notes"`
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
