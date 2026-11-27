package design

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// variantsVector 是 schema/testdata/design-variants.json 的一条用例（引擎的 variants.mjs 读同一份）。
type variantsVector struct {
	Name   string          `json:"name"`
	Raw    string          `json:"raw"`
	Frames []string        `json:"frames"`
	Themes []string        `json:"themes"`
	Want   json.RawMessage `json:"want"`
	Error  *string         `json:"error"`
}

func variantsVectorsPath() string {
	return filepath.Join("..", "..", "..", "schema", "testdata", "design-variants.json")
}

func loadVariantsVectors(t *testing.T) []variantsVector {
	t.Helper()
	b, err := os.ReadFile(variantsVectorsPath())
	require.NoError(t, err)
	var vs []variantsVector
	require.NoError(t, json.Unmarshal(b, &vs))
	require.NotEmpty(t, vs)
	return vs
}

func TestParseVariantsVectors(t *testing.T) {
	for _, v := range loadVariantsVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			require.True(t, (v.Error != nil) != (v.Want != nil), "用例必须恰好给 want 或 error 之一")
			got, err := ParseVariants([]byte(v.Raw), v.Frames, v.Themes)
			if v.Error != nil {
				var ve *VariantsError
				require.True(t, errors.As(err, &ve), "应报错，实际 %v", err)
				assert.Equal(t, *v.Error, ve.Path, ve.Error())
				return
			}
			require.NoError(t, err)
			b, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, string(v.Want), string(b))
		})
	}
}

// TestParseVariantsEngineTwin 用 node 跑引擎的 variants.mjs，逐例对照同一份向量（没有 node 时跳过）。
func TestParseVariantsEngineTwin(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过引擎实现的对照")
	}
	mod, err := filepath.Abs(filepath.Join("..", "..", "designengine", "engine", "variants.mjs"))
	require.NoError(t, err)
	vectors, err := filepath.Abs(variantsVectorsPath())
	require.NoError(t, err)
	script := `
const { parseVariants, VariantsError } = await import(process.argv[1]);
const fs = await import("node:fs");
const vs = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const out = vs.map((v) => {
	try { return { want: parseVariants(v.raw, v.frames, v.themes) }; }
	catch (e) { if (e instanceof VariantsError) return { error: e.path }; throw e; }
});
process.stdout.write(JSON.stringify(out));
`
	cmd := exec.Command(node, "--input-type=module", "-e", script, "file://"+mod, vectors)
	stdout, err := cmd.Output()
	require.NoError(t, err)
	var got []struct {
		Want  json.RawMessage `json:"want"`
		Error *string         `json:"error"`
	}
	require.NoError(t, json.Unmarshal(stdout, &got))
	vs := loadVariantsVectors(t)
	require.Len(t, got, len(vs))
	for i, v := range vs {
		if v.Error != nil {
			require.NotNil(t, got[i].Error, "%s：引擎应报错", v.Name)
			assert.Equal(t, *v.Error, *got[i].Error, v.Name)
			continue
		}
		require.Nil(t, got[i].Error, "%s：引擎不应报错", v.Name)
		assert.JSONEq(t, string(v.Want), string(got[i].Want), v.Name)
	}
}
