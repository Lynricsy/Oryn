package coord

import "testing"

func TestAPISpec(t *testing.T) {
	routes := apiRoutes()
	if len(routes) != 390 || len(apiOps()) != 390 {
		t.Fatalf("routes = %d, ops = %d, want 390", len(routes), len(apiOps()))
	}
	up := routes["updateProject"]
	if up.Method != "PATCH" || up.Path != "/api/v1/projects/{id}" {
		t.Fatalf("updateProject = %s %s", up.Method, up.Path)
	}
	if len(up.PathParams) != 1 || up.PathParams[0].Name != "id" {
		t.Fatalf("updateProject path params = %+v", up.PathParams)
	}
	if up.BodyType != "application/json" {
		t.Fatalf("updateProject body type = %q", up.BodyType)
	}
	props, _ := up.BodySchema["properties"].(map[string]any)
	if _, ok := props["githubGates"]; !ok {
		t.Fatalf("updateProject body properties lack githubGates: %v", up.BodySchema)
	}
	if bt := routes["putAgentConfigFile"].BodyType; bt != "application/octet-stream" {
		t.Fatalf("putAgentConfigFile body type = %q", bt)
	}
}
