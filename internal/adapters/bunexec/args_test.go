package bunexec

import (
	"reflect"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestBuildCompileArgs(t *testing.T) {
	const (
		entrypoint = "/proj/.svelte-kit/jesterkit-sveltekit/temp-server/index.ts"
		outfile    = "/work/out/app-linux-amd64"
	)

	tests := []struct {
		name              string
		target            string
		minify, sourcemap bool
		want              []string
	}{
		{
			name:   "neither flag (release default off)",
			target: "bun-linux-x64", minify: false, sourcemap: false,
			want: []string{"build", "--compile", "--target=bun-linux-x64", "--outfile=" + outfile, entrypoint},
		},
		{
			name:   "minify only",
			target: "bun-linux-x64", minify: true, sourcemap: false,
			want: []string{"build", "--compile", "--target=bun-linux-x64", "--outfile=" + outfile, "--minify", entrypoint},
		},
		{
			name:   "sourcemap only",
			target: "bun-linux-arm64", minify: false, sourcemap: true,
			want: []string{"build", "--compile", "--target=bun-linux-arm64", "--outfile=" + outfile, "--sourcemap", entrypoint},
		},
		{
			name:   "both flags (pokkum release default)",
			target: "bun-linux-arm64", minify: true, sourcemap: true,
			want: []string{"build", "--compile", "--target=bun-linux-arm64", "--outfile=" + outfile, "--minify", "--sourcemap", entrypoint},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCompileArgs(entrypoint, outfile, tt.target, tt.minify, tt.sourcemap)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildCompileArgs() =\n  %#v\nwant\n  %#v", got, tt.want)
			}
		})
	}
}

// TestBuildCompileArgsForSupportedPlatforms locks in the exact argv Compile
// generates for both platforms Pokkum ships, at the default CompileRequest
// settings (Minify true, Sourcemap false — matching core's release default
// once core inverts CompileOptions.NoMinify into ports.CompileRequest.Minify).
func TestBuildCompileArgsForSupportedPlatforms(t *testing.T) {
	entrypoint := "/proj/.svelte-kit/jesterkit-sveltekit/temp-server/index.ts"

	amd64Target, ok := ports.LinuxAMD64.BunTarget()
	if !ok {
		t.Fatal("ports.LinuxAMD64.BunTarget() reported no mapping")
	}
	arm64Target, ok := ports.LinuxARM64.BunTarget()
	if !ok {
		t.Fatal("ports.LinuxARM64.BunTarget() reported no mapping")
	}

	if got, want := amd64Target, "bun-linux-x64"; got != want {
		t.Fatalf("linux/amd64 BunTarget() = %q, want %q", got, want)
	}
	if got, want := arm64Target, "bun-linux-arm64"; got != want {
		t.Fatalf("linux/arm64 BunTarget() = %q, want %q", got, want)
	}

	gotAMD64 := buildCompileArgs(entrypoint, "/work/out/app-linux-amd64", amd64Target, true, false)
	wantAMD64 := []string{"build", "--compile", "--target=bun-linux-x64", "--outfile=/work/out/app-linux-amd64", "--minify", entrypoint}
	if !reflect.DeepEqual(gotAMD64, wantAMD64) {
		t.Errorf("linux/amd64 argv =\n  %#v\nwant\n  %#v", gotAMD64, wantAMD64)
	}

	gotARM64 := buildCompileArgs(entrypoint, "/work/out/app-linux-arm64", arm64Target, true, false)
	wantARM64 := []string{"build", "--compile", "--target=bun-linux-arm64", "--outfile=/work/out/app-linux-arm64", "--minify", entrypoint}
	if !reflect.DeepEqual(gotARM64, wantARM64) {
		t.Errorf("linux/arm64 argv =\n  %#v\nwant\n  %#v", gotARM64, wantARM64)
	}
}
