package file

import "testing"

func TestSortDirsFirstCaseInsensitive(t *testing.T) {
	files := []Info{
		{Name: "Zebra"},
		{Name: "src", IsDir: true},
		{Name: "apple"},
		{Name: "Build", IsDir: true},
	}
	Sort(files)

	var got []string
	for _, f := range files {
		got = append(got, f.Name)
	}
	// Directories first (case-insensitive: Build before src), then files
	// (case-insensitive: apple before Zebra).
	want := []string{"Build", "src", "apple", "Zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSortStableForEqualNames(t *testing.T) {
	// Same dir-ness and case-insensitively equal names keep their input order
	// (Sort uses a stable sort). Size is used here only to tell them apart.
	files := []Info{
		{Name: "README", Size: 1},
		{Name: "readme", Size: 2},
	}
	Sort(files)
	if files[0].Size != 1 || files[1].Size != 2 {
		t.Errorf("stable order not preserved: %+v", files)
	}
}
