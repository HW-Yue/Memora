package row

type RelationEndpoint struct {
	Database string
	Table    string
	RowID    string
}

type RelationDefinition struct {
	Source      RelationEndpoint
	Type        string
	Target      RelationEndpoint
	Description string
}
