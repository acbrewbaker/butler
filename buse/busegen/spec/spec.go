package spec

type Spec struct {
	Requests      []*RequestSpec      `json:"requests"`
	Notifications []*NotificationSpec `json:"notifications"`
	StructTypes   []*StructTypeSpec   `json:"types"`
	EnumTypes     []*EnumTypeSpec     `json:"types"`
	VersionNote   string              `json:"versionNote"`
}

type RequestSpec struct {
	Method string      `json:"method"`
	Doc    string      `json:"doc"`
	Caller string      `json:"caller"`
	Params *StructSpec `json:"params"`
	Result *StructSpec `json:"result"`
}

type StructTypeSpec struct {
	Name   string       `json:"name"`
	Doc    string       `json:"doc"`
	Fields []*FieldSpec `json:"fields"`
}

type EnumTypeSpec struct {
	Name   string           `json:"name"`
	Doc    string           `json:"doc"`
	Values []*EnumValueSpec `json:"values"`
}

type EnumValueSpec struct {
	Name  string `json:"name"`
	Doc   string `json:"doc"`
	Value string `json:"value"`
}

type StructSpec struct {
	Fields []*FieldSpec `json:"fields"`
}

type FieldSpec struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
	Type string `json:"type"`
}

type NotificationSpec struct {
	Method string      `json:"method"`
	Doc    string      `json:"doc"`
	Params *StructSpec `json:"params"`
}
