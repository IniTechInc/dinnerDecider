package vision

import (
	"math"
	"testing"
)

const rectEpsilon = 1e-6

func rectsClose(a, b Rect) bool {
	return math.Abs(a.X-b.X) < rectEpsilon &&
		math.Abs(a.Y-b.Y) < rectEpsilon &&
		math.Abs(a.W-b.W) < rectEpsilon &&
		math.Abs(a.H-b.H) < rectEpsilon
}

// The headline assertion of this file. `CropService.tileGrid` calls itself a
// 3x3 grid in its own doc comment and emits 16 tiles. Every one of the three
// sizes the pipeline doc verified must come back as 16, not 9.
func TestTileGridEmitsSixteenTilesNotNine(t *testing.T) {
	sizes := []struct {
		name string
		w, h int
	}{
		{"landscape 2016x1512", 2016, 1512},
		{"portrait 1512x2016", 1512, 2016},
		{"square 900x900", 900, 900},
	}
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			tiles := TileGrid(size.w, size.h)
			if len(tiles) != 16 {
				t.Fatalf("TileGrid(%d, %d) = %d tiles, want exactly 16 (a 3x3 port emits 9 and is not a parity implementation)", size.w, size.h, len(tiles))
			}
		})
	}
}

// 16 tiles could in principle be any shape. Pin it to 4 rows of 4.
func TestTileGridIsFourColumnsByFourRows(t *testing.T) {
	sizes := [][2]int{{2016, 1512}, {1512, 2016}, {900, 900}}
	for _, size := range sizes {
		tiles := TileGrid(size[0], size[1])
		rows := map[float64][]Rect{}
		var rowOrder []float64
		for _, tile := range tiles {
			if _, seen := rows[tile.Y]; !seen {
				rowOrder = append(rowOrder, tile.Y)
			}
			rows[tile.Y] = append(rows[tile.Y], tile)
		}
		if len(rowOrder) != 4 {
			t.Fatalf("TileGrid(%d, %d): %d distinct rows, want 4", size[0], size[1], len(rowOrder))
		}
		for _, y := range rowOrder {
			if len(rows[y]) != 4 {
				t.Fatalf("TileGrid(%d, %d): row y=%v has %d columns, want 4", size[0], size[1], y, len(rows[y]))
			}
		}
	}
}

// The cursor's fourth and last stop is at 0.8 of the image, so the remainder
// tile it emits is exactly the trailing 20% of the image. This is the shape
// that gives the grid away as 4x4: a genuine 3x3 grid has no remainder tiles.
func TestTileGridLastColumnAndRowAreTwentyPercentRemainders(t *testing.T) {
	sizes := [][2]int{{2016, 1512}, {1512, 2016}, {900, 900}}
	for _, size := range sizes {
		w := float64(size[0])
		h := float64(size[1])
		tiles := TileGrid(size[0], size[1])

		last := tiles[len(tiles)-1]
		if math.Abs(last.W-0.2*w) > rectEpsilon {
			t.Errorf("TileGrid(%v): last column width = %v, want %v (20%% of the image width)", size, last.W, 0.2*w)
		}
		if math.Abs(last.H-0.2*h) > rectEpsilon {
			t.Errorf("TileGrid(%v): last row height = %v, want %v (20%% of the image height)", size, last.H, 0.2*h)
		}
		// The remainder tiles finish flush with the far edges, so the grid
		// really does cover the whole image.
		if math.Abs((last.X+last.W)-w) > rectEpsilon {
			t.Errorf("TileGrid(%v): last tile right edge = %v, want %v", size, last.X+last.W, w)
		}
		if math.Abs((last.Y+last.H)-h) > rectEpsilon {
			t.Errorf("TileGrid(%v): last tile bottom edge = %v, want %v", size, last.Y+last.H, h)
		}
		// Every full tile is exactly a third of the image.
		if math.Abs(tiles[0].W-w/3) > rectEpsilon || math.Abs(tiles[0].H-h/3) > rectEpsilon {
			t.Errorf("TileGrid(%v): first tile = %vx%v, want %vx%v", size, tiles[0].W, tiles[0].H, w/3, h/3)
		}
	}
}

// Row-major, x on the inner loop. Emission order is contractual because the
// scan budget truncates the list.
func TestTileGridEmissionOrderIsRowMajorXInner(t *testing.T) {
	tiles := TileGrid(2016, 1512)
	for i := range tiles {
		if i%4 == 0 {
			continue
		}
		prev := tiles[i-1]
		cur := tiles[i]
		if cur.Y != prev.Y {
			t.Fatalf("tile %d: y jumped from %v to %v mid-row; emission must be row-major", i, prev.Y, cur.Y)
		}
		if cur.X <= prev.X {
			t.Fatalf("tile %d: x went from %v to %v; x must increase on the inner loop", i, prev.X, cur.X)
		}
	}
	for row := 1; row < 4; row++ {
		if tiles[row*4].Y <= tiles[(row-1)*4].Y {
			t.Fatalf("row %d starts at y=%v, not after row %d at y=%v", row, tiles[row*4].Y, row-1, tiles[(row-1)*4].Y)
		}
		if tiles[row*4].X != 0 {
			t.Fatalf("row %d starts at x=%v, want 0", row, tiles[row*4].X)
		}
	}
}

// The scan budget is 4 calls: one whole-image pass plus three crops. So only
// the first THREE tiles in emission order are ever sent to the model, and with
// a 4x4 grid all three sit in the top row. That is the real geometry of a scan
// and the reason emission order is pinned rather than treated as incidental.
func TestTileGridFirstThreeAnalysedTilesAreTheTopRow(t *testing.T) {
	tiles := TileGrid(2016, 1512)
	want := []Rect{
		{X: 0, Y: 0, W: 672, H: 504},
		{X: 537.6, Y: 0, W: 672, H: 504},
		{X: 1075.2, Y: 0, W: 672, H: 504},
	}
	for i, expected := range want {
		if !rectsClose(tiles[i], expected) {
			t.Errorf("analysed tile %d = %+v, want %+v", i, tiles[i], expected)
		}
	}
}

// 16 is emergent, not hardcoded: the `> 8` guard prunes remainder tiles once
// they get small, and a tiny image drops out entirely.
func TestTileGridSmallImagesEmitFewerTiles(t *testing.T) {
	cases := []struct {
		name  string
		w, h  int
		count int
	}{
		// 30/3 = 10, step 8. Stops at x = 0, 8, 16, 24; the tile at 24 is
		// only 6px wide, fails `> 8`, and is dropped — 3x3 = 9 survive.
		{"30x30 prunes the remainder tiles", 30, 30, 9},
		// 10/3 = 3.33, never greater than 8: no tile is usable at all.
		{"10x10 yields nothing", 10, 10, 0},
		{"1x1 yields nothing", 1, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(TileGrid(c.w, c.h)); got != c.count {
				t.Fatalf("TileGrid(%d, %d) = %d tiles, want %d", c.w, c.h, got, c.count)
			}
		})
	}
}

func TestTileGridRejectsNonPositiveDimensions(t *testing.T) {
	for _, c := range [][2]int{{0, 0}, {0, 100}, {100, 0}, {-1, -1}, {-2016, 1512}} {
		if tiles := TileGrid(c[0], c[1]); tiles != nil {
			t.Errorf("TileGrid(%d, %d) = %v, want nil", c[0], c[1], tiles)
		}
	}
}

func TestRectIntegral(t *testing.T) {
	cases := []struct {
		name string
		in   Rect
		want Rect
	}{
		{"already whole pixels is unchanged", Rect{0, 0, 672, 504}, Rect{0, 0, 672, 504}},
		{"origin floors, far edge ceils", Rect{0.5, 0.5, 1, 1}, Rect{0, 0, 2, 2}},
		{"tile 1 of a 2016px row", Rect{537.6, 0, 672, 504}, Rect{537, 0, 673, 504}},
		// The remainder tile ends flush on the far edge (1612.8+403.2 = 2016
		// exactly), so ceil adds nothing there — but the origin still floors,
		// which is what widens the rect by one pixel.
		{"remainder tile grows only at the origin", Rect{1612.8, 1209.6, 403.2, 302.4}, Rect{1612, 1209, 404, 303}},
		{"fraction entirely inside one pixel still spans one", Rect{2.2, 3.3, 0.4, 0.4}, Rect{2, 3, 1, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Integral()
			if !rectsClose(got, c.want) {
				t.Fatalf("Rect%+v.Integral() = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// The specific trap: naive int truncation of origin and size is NOT integral,
// and the difference shows up on every tile except the first.
func TestRectIntegralIsNotIntTruncation(t *testing.T) {
	r := Rect{X: 537.6, Y: 0, W: 672, H: 504}
	got := r.Integral()
	truncated := Rect{X: float64(int(r.X)), Y: float64(int(r.Y)), W: float64(int(r.W)), H: float64(int(r.H))}
	if rectsClose(got, truncated) {
		t.Fatalf("Integral() matched int truncation (%+v); it must round the far edge UP, giving width 673", truncated)
	}
	if got.W != 673 {
		t.Fatalf("Integral().W = %v, want 673", got.W)
	}
}

// After Integral the tiles are still 16 and still all large enough for the
// `>= 8x8` check crop.go applies.
func TestTileGridSurvivesIntegralAndTheMinimumSizeCheck(t *testing.T) {
	for _, size := range [][2]int{{2016, 1512}, {1512, 2016}, {900, 900}} {
		for i, tile := range TileGrid(size[0], size[1]) {
			integral := tile.Integral()
			if integral.W < 8 || integral.H < 8 {
				t.Errorf("TileGrid(%v) tile %d integral = %+v, below the 8x8 minimum", size, i, integral)
			}
		}
	}
}
