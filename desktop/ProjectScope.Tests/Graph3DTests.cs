#nullable enable
using System.Diagnostics;
using Nexus.Core;
using Xunit;

public class Graph3DTests
{
    private static (int N, int[] Edges, string[] Ids) Star(int leaves)
    {
        var edges = new List<int>();
        var ids = new List<string> { "session:hub" };
        for (var i = 0; i < leaves; i++)
        {
            ids.Add($"memory:m{i}");
            edges.Add(0);
            edges.Add(i + 1);
        }
        return (ids.Count, edges.ToArray(), ids.ToArray());
    }

    /// <summary>A graph shaped like real projects: chat stars, file fans, isolates.</summary>
    private static (int N, int[] Edges, string[] Ids) Big(int n)
    {
        var ids = new string[n];
        var edges = new List<int>();
        for (var i = 0; i < n; i++) ids[i] = $"memory:{i}";
        var hub = 0;
        for (var i = 1; i < n; i++)
        {
            if (i % 25 == 0) { hub = i; continue; }
            if (i % 7 == 3) continue; // unlinked
            edges.Add(hub);
            edges.Add(i);
            if (i % 11 == 0 && i > 30)
            {
                edges.Add(i);
                edges.Add(i - 30);
            }
        }
        return (n, edges.ToArray(), ids);
    }

    [Fact]
    public void ProjectsTargetToCentreAndOffsetsByFocal()
    {
        var cam = new Camera3D { Distance = 100 };
        var c = cam.Project(0, 0, 0, 800, 600);
        Assert.True(c.Visible);
        Assert.Equal(400, c.X, 6);
        Assert.Equal(300, c.Y, 6);
        Assert.Equal(100, c.Depth, 6);

        var focal = cam.Focal(600);
        var right = cam.Project(10, 0, 0, 800, 600);
        Assert.Equal(400 + 10 * focal / 100, right.X, 6);
        Assert.Equal(300, right.Y, 6);
        var up = cam.Project(0, 10, 0, 800, 600);
        Assert.True(up.Y < 300); // world up is screen up

        // Nearer points project bigger.
        var near = cam.Project(0, 0, 50, 800, 600);
        Assert.Equal(50, near.Depth, 6);
        Assert.True(near.Scale > c.Scale);

        // Behind the camera: not visible.
        Assert.False(cam.Project(0, 0, 150, 800, 600).Visible);
    }

    [Fact]
    public void YawTurnsAroundTheTarget()
    {
        var cam = new Camera3D { Distance = 100, Yaw = Math.PI / 2 };
        // Camera now sits on +x looking toward −x: a point at +x is nearest.
        var p = cam.Project(30, 0, 0, 800, 600);
        Assert.Equal(70, p.Depth, 6);
        Assert.Equal(400, p.X, 6);
        // And +z is to the left of screen centre.
        Assert.True(cam.Project(0, 0, 30, 800, 600).X < 400);
    }

    [Fact]
    public void PanMovesContentWithThePointer()
    {
        var cam = new Camera3D { Distance = 300, Yaw = 0.7, Pitch = 0.3 };
        var before = cam.Project(0, 0, 0, 800, 600);
        cam.Pan(40, -25, 600);
        var after = cam.Project(0, 0, 0, 800, 600);
        Assert.Equal(before.X + 40, after.X, 4);
        Assert.Equal(before.Y - 25, after.Y, 4);
    }

    [Fact]
    public void ZoomKeepsThePointUnderTheCursor()
    {
        var cam = new Camera3D { Distance = 400, Yaw = -0.4, Pitch = 0.2 };
        const double w = 900, h = 600, sx = 650, sy = 180;
        // The world point under the cursor on the target plane.
        var (ox, oy, oz) = cam.ScreenDeltaToWorld(sx - w / 2, sy - h / 2, cam.Distance, h);
        var (qx, qy, qz) = (cam.TargetX + ox, cam.TargetY + oy, cam.TargetZ + oz);
        cam.ZoomAt(sx, sy, 1.8, w, h);
        Assert.Equal(400 / 1.8, cam.Distance, 6);
        var p = cam.Project(qx, qy, qz, w, h);
        Assert.Equal(sx, p.X, 4);
        Assert.Equal(sy, p.Y, 4);
    }

    [Fact]
    public void FitDistanceShowsTheWholeSphere()
    {
        var cam = new Camera3D();
        cam.Distance = cam.FitDistance(200, 800, 600);
        // A point on the sphere's edge sideways and upwards stays on screen.
        foreach (var (x, y) in new[] { (200.0, 0.0), (-200.0, 0.0), (0.0, 200.0), (0.0, -200.0) })
        {
            var p = cam.Project(x, y, 0, 800, 600);
            Assert.InRange(p.X, 0, 800);
            Assert.InRange(p.Y, 0, 600);
        }
    }

    [Fact]
    public void LerpTakesTheShortWayRound()
    {
        var a = new Camera3D { Yaw = 3.0, Distance = 100 };
        var b = new Camera3D { Yaw = -3.0, Distance = 400, TargetX = 10 };
        var mid = Camera3D.Lerp(a, b, 0.5);
        Assert.True(Math.Abs(Math.Abs(mid.Yaw) - Math.PI) < 0.01);
        Assert.Equal(200, mid.Distance, 6); // geometric mean
        Assert.Equal(5, mid.TargetX, 6);
        Assert.Equal(b.Distance, Camera3D.Lerp(a, b, 1).Distance, 6);
    }

    [Fact]
    public void HitTestPicksTheNearestInDepth()
    {
        double[] sx = [100, 104, 300];
        double[] sy = [100, 101, 300];
        double[] r = [10, 10, 10];
        double[] depth = [500, 200, 50];
        bool[] shown = [true, true, true];
        Assert.Equal(1, Graph3D.HitTest(sx, sy, r, depth, shown, 102, 100));
        shown[1] = false;
        Assert.Equal(0, Graph3D.HitTest(sx, sy, r, depth, shown, 102, 100));
        Assert.Equal(-1, Graph3D.HitTest(sx, sy, r, depth, shown, 200, 200));
        Assert.Equal(2, Graph3D.HitTest(sx, sy, r, depth, shown, 311, 300, slack: 2));
    }

    [Fact]
    public void DepthOrderIsFarthestFirst()
    {
        double[] depth = [5, 50, 20, 70];
        bool[] shown = [true, true, false, true];
        var order = new int[4];
        var keys = new double[4];
        var count = Graph3D.DepthOrder(depth, shown, order, keys);
        Assert.Equal(3, count);
        Assert.Equal(new[] { 3, 1, 0 }, order[..3]);
        Assert.Equal(1, Graph3D.DepthCue(10, 10, 90), 6);
        Assert.Equal(0, Graph3D.DepthCue(90, 10, 90), 6);
        Assert.Equal(0.5, Graph3D.DepthCue(50, 10, 90), 6);
    }

    [Fact]
    public void LayoutIsDeterministicAndSettles()
    {
        var (n, edges, ids) = Star(15);
        var a = new ForceLayout3D(n, edges, ids);
        var b = new ForceLayout3D(n, edges, ids);
        Assert.Equal(a.Positions, b.Positions);
        var ticks = a.RunToRest();
        b.RunToRest();
        Assert.Equal(a.Positions, b.Positions);
        Assert.False(a.Active);
        Assert.InRange(ticks, 100, 400);
        Assert.All(a.Positions, v => Assert.True(double.IsFinite(v)));

        // Leaves end up near link length from the hub, and not on top of each other.
        for (var i = 1; i < n; i++)
        {
            var d = Dist(a.Positions, 0, i);
            Assert.InRange(d, ForceLayout3D.LinkDistance * 0.5, ForceLayout3D.LinkDistance * 3);
        }
        for (var i = 1; i < n; i++)
            for (var j = i + 1; j < n; j++)
                Assert.True(Dist(a.Positions, i, j) > 5);

        // The layout really uses depth: z spread is comparable to x spread.
        var zs = Enumerable.Range(0, n).Select(i => a.Positions[i * 3 + 2]).ToArray();
        var xs = Enumerable.Range(0, n).Select(i => a.Positions[i * 3]).ToArray();
        Assert.True(zs.Max() - zs.Min() > (xs.Max() - xs.Min()) * 0.3);
    }

    [Fact]
    public void PinnedNodeStaysPut()
    {
        var (n, edges, ids) = Star(8);
        var sim = new ForceLayout3D(n, edges, ids);
        sim.Pin(3, 200, 10, -40);
        for (var t = 0; t < 50; t++) sim.Tick();
        Assert.Equal(200, sim.Positions[9]);
        Assert.Equal(10, sim.Positions[10]);
        Assert.Equal(-40, sim.Positions[11]);
        sim.Unpin(3);
        sim.Tick();
        Assert.NotEqual(200, sim.Positions[9]);
    }

    [Fact]
    public void ClustersStayApartFromEachOther()
    {
        // Two separate stars: each cluster's members sit closer to their own centroid.
        var edges = new List<int>();
        var ids = new List<string>();
        for (var s = 0; s < 2; s++)
        {
            var hub = ids.Count;
            ids.Add($"session:{s}");
            for (var i = 0; i < 10; i++) { edges.Add(hub); edges.Add(ids.Count); ids.Add($"memory:{s}-{i}"); }
        }
        var sim = new ForceLayout3D(ids.Count, edges.ToArray(), ids.ToArray());
        sim.RunToRest();
        var hubGap = Dist(sim.Positions, 0, 11);
        Assert.True(hubGap > ForceLayout3D.LinkDistance * 1.5, $"hubs {hubGap:0} apart");
    }

    [Fact]
    public void ThousandNodesSettleQuickly()
    {
        var (n, edges, ids) = Big(1000);
        var sim = new ForceLayout3D(n, edges, ids);
        var watch = Stopwatch.StartNew();
        var ticks = sim.RunToRest();
        watch.Stop();
        Assert.False(sim.Active);
        Assert.All(sim.Positions, v => Assert.True(double.IsFinite(v)));
        // ~240 ticks of the full simulation; generous for slow CI machines.
        Assert.True(watch.Elapsed.TotalSeconds < 6, $"{ticks} ticks took {watch.Elapsed.TotalMilliseconds:0} ms");
        // Per frame budget: two ticks must fit comfortably off the UI thread.
        Assert.True(watch.Elapsed.TotalMilliseconds / Math.Max(1, ticks) < 25);

        // Projection + depth order + hit test of 1000 nodes per frame is cheap.
        var cam = new Camera3D();
        var shown = Enumerable.Repeat(true, n).ToArray();
        var (cx, cy, cz, r) = Graph3D.Bounds(sim.Positions, shown);
        cam.TargetX = cx; cam.TargetY = cy; cam.TargetZ = cz;
        cam.Distance = cam.FitDistance(r, 1200, 800);
        double[] sx = new double[n], sy = new double[n], sr = new double[n], depth = new double[n];
        var order = new int[n];
        var keys = new double[n];
        watch.Restart();
        for (var frame = 0; frame < 120; frame++)
        {
            cam.Orbit(0.01, 0);
            var view = new Camera3D.View(cam, 1200, 800);
            for (var i = 0; i < n; i++)
            {
                var p = view.Project(sim.Positions[i * 3], sim.Positions[i * 3 + 1], sim.Positions[i * 3 + 2]);
                sx[i] = p.X; sy[i] = p.Y; depth[i] = p.Depth; sr[i] = 6 * p.Scale;
            }
            Graph3D.DepthOrder(depth, shown, order, keys);
            Graph3D.HitTest(sx, sy, sr, depth, shown, 600, 400);
        }
        Assert.True(watch.Elapsed.TotalMilliseconds / 120 < 4, $"frame math {watch.Elapsed.TotalMilliseconds / 120:0.00} ms");
    }

    [Fact]
    public void SpriteRectCentresTheBodyOnTheProjectedPoint()
    {
        var (left, top, size) = Graph3D.SpriteRect(300, 200, 10);
        Assert.Equal(40, size, 6);
        Assert.Equal(300, left + size / 2, 6);
        Assert.Equal(200, top + size / 2, 6);
        // The body edge (radius 10 px) sits a quarter of the image from the centre.
        Assert.Equal(10, size * NodeSprite.BodyFraction, 6);
        // Different points never collapse to one spot.
        Assert.NotEqual(left, Graph3D.SpriteRect(10, 20, 10).Left);
    }

    [Fact]
    public void EdgeQuadRunsBetweenTheEndpoints()
    {
        var (left, top, length, angle) = Graph3D.EdgeQuad(10, 20, 13, 24, 2);
        Assert.Equal(5, length, 6);
        Assert.Equal(10, left, 6);
        Assert.Equal(19, top, 6);
        // Rotating (length, 0) about (0, thickness/2) lands on the end point.
        Assert.Equal(13, left + Math.Cos(angle) * length, 6);
        Assert.Equal(24, top + 1 + Math.Sin(angle) * length, 6);
    }

    [Fact]
    public void FitCentresTheGraphInTheVisibleAreaWithLabels()
    {
        var (n, edges, ids) = Big(200);
        var sim = new ForceLayout3D(n, edges, ids);
        sim.RunToRest();
        var shown = Enumerable.Repeat(true, n).ToArray();
        var radius = Enumerable.Repeat(5.0, n).ToArray();
        var labels = new double[n];
        labels[0] = 120; // a long label on the hub
        var cam = new Camera3D { Yaw = 0.8, Pitch = 0.3, TargetX = 900, Distance = 50 }; // far off to start
        const double w = 1000, h = 700, right = 300, bottom = 50, pad = 24;
        Graph3D.FitToScreen(cam, sim.Positions, shown, radius, labels, w, h, 0, 0, right, bottom, pad);
        var box = Graph3D.ScreenBox(cam, sim.Positions, shown, radius, labels, w, h, double.MaxValue, Graph3D.OutlierTrim)!.Value;
        // Everything (labels too) inside the area left of the panel and above the legend.
        Assert.True(box.Left >= pad - 1, $"left {box.Left:0.0}");
        Assert.True(box.Top >= pad - 1, $"top {box.Top:0.0}");
        Assert.True(box.Right <= w - right - pad + 1, $"right {box.Right:0.0}");
        Assert.True(box.Bottom <= h - bottom - pad + 1, $"bottom {box.Bottom:0.0}");
        // Centred in that area.
        var areaCx = (pad + w - right - pad) / 2;
        var areaCy = (pad + h - bottom - pad) / 2;
        Assert.InRange((box.Left + box.Right) / 2, areaCx - 8, areaCx + 8);
        Assert.InRange((box.Top + box.Bottom) / 2, areaCy - 8, areaCy + 8);
        // And it fills the area on at least one axis (not a tiny speck).
        var fill = Math.Max((box.Right - box.Left) / (w - right - 2 * pad), (box.Bottom - box.Top) / (h - bottom - 2 * pad));
        Assert.True(fill > 0.9, $"fill {fill:0.00}");
    }

    private static double FitFill(double[] pos, bool[] shown, double[] radius, double[] labels, double w, double h, double right, double bottom)
    {
        var cam = new Camera3D { Yaw = 0.3, Pitch = 0.2 };
        Graph3D.FitToScreen(cam, pos, shown, radius, labels, w, h, 0, 0, right, bottom);
        // How much of the area the graph's own nodes (no labels) span.
        var box = Graph3D.ScreenBox(cam, pos, shown, radius, new double[shown.Length], w, h, double.MaxValue, Graph3D.OutlierTrim)!.Value;
        var (l, t, r, b) = Graph3D.FitArea(w, h, 0, 0, right, bottom, 24);
        return Math.Max((box.Right - box.Left) / (r - l), (box.Bottom - box.Top) / (b - t));
    }

    [Fact]
    public void FitIgnoresFarOutliers()
    {
        var (n, edges, ids) = Big(200);
        var sim = new ForceLayout3D(n, edges, ids);
        sim.RunToRest();
        var pos = (double[])sim.Positions.Clone();
        // Two stray nodes far away would otherwise shrink everything else.
        pos[3 * 3] = 40000;
        pos[10 * 3 + 1] = -30000;
        var shown = Enumerable.Repeat(true, n).ToArray();
        var fill = FitFill(pos, shown, Enumerable.Repeat(5.0, n).ToArray(), new double[n], 1000, 700, 0, 50);
        Assert.True(fill > 0.75, $"fill {fill:0.00}");
    }

    [Fact]
    public void FitDoesNotCollapseUnderLongLabelsOrAWidePanel()
    {
        var (n, edges, ids) = Big(200);
        var sim = new ForceLayout3D(n, edges, ids);
        sim.RunToRest();
        var shown = Enumerable.Repeat(true, n).ToArray();
        var radius = Enumerable.Repeat(5.0, n).ToArray();
        // Every node with a label wider than the whole view.
        var huge = Enumerable.Repeat(2000.0, n).ToArray();
        Assert.True(FitFill(sim.Positions, shown, radius, huge, 760, 540, 306, 52) > 0.6);
        // A panel covering almost all of the view: the inset is ignored, not obeyed into a speck.
        Assert.True(FitFill(sim.Positions, shown, radius, new double[n], 760, 540, 700, 52) > 0.75);
        var (l, _, r, _) = Graph3D.FitArea(760, 540, 0, 0, 700, 52, 24);
        Assert.True(r - l > 600);
        // A reasonable panel is respected.
        var (l2, _, r2, _) = Graph3D.FitArea(760, 540, 0, 0, 306, 52, 24);
        Assert.Equal(760 - 306 - 24, r2, 6);
        Assert.Equal(24, l2, 6);
    }

    [Fact]
    public void LabelsFlipLeftInsteadOfClipping()
    {
        // Room on the right: label to the right of the node.
        Assert.Equal(115, Graph3D.LabelX(100, 10, 80, 0, 500), 6);
        // Near the right edge (or a panel at 450): flips to the left side.
        Assert.Equal(420 - 10 - 5 - 80, Graph3D.LabelX(420, 10, 80, 0, 450), 6);
        // No room on either side: kept fully inside the range.
        var x = Graph3D.LabelX(60, 10, 100, 0, 130);
        Assert.InRange(x, 0, 30);
        Assert.InRange(x + 100, 0, 130);
    }

    [Fact]
    public void TwoDLabelsStayInsideTheView()
    {
        var requests = new[]
        {
            new GraphLayout.LabelRequest(5, 100, 6, 120, 20, Forced: false),   // node at the left edge
            new GraphLayout.LabelRequest(395, 50, 6, 120, 20, Forced: false),  // node at the right edge
            new GraphLayout.LabelRequest(30, 290, 6, 150, 20, Forced: true),   // bottom-left corner, forced
        };
        var slots = GraphLayout.PlaceLabels(requests, 400, 300);
        for (var k = 0; k < slots.Length; k++)
        {
            Assert.True(slots[k].Placed);
            Assert.InRange(slots[k].X, 4, 396 - requests[k].Width);
            Assert.InRange(slots[k].Y, 4, 296 - 20);
        }
        // Without a viewport the old behaviour holds: right of the node.
        Assert.Equal(15, GraphLayout.PlaceLabels(requests)[0].X, 6);
    }

    [Fact]
    public void StripNoticesKeepsTheFirstRealLine()
    {
        Assert.Equal("Fix the login bug", GraphLayout.StripNotices("[SYSTEM NOTIFICATION - NOT USER INPUT]\nFix the login bug"));
        Assert.Equal("Fix it", GraphLayout.StripNotices("[SYSTEM NOTIFICATION - NOT USER INPUT] Fix it"));
        Assert.Equal("", GraphLayout.StripNotices("[SYSTEM NOTIFICATION - NOT"));
        Assert.Equal("done", GraphLayout.StripNotices("[TASK NOTIFICATION] done\n"));
        Assert.Equal("Plain title", GraphLayout.StripNotices("Plain title"));
        Assert.Equal("[draft] notes", GraphLayout.StripNotices("[draft] notes"));
    }

    [Fact]
    public void PlaceTargetAtMovesTheTargetOnScreen()
    {
        var cam = new Camera3D { Distance = 300, Yaw = 1.1, Pitch = -0.2, TargetX = 5, TargetY = 6, TargetZ = 7 };
        var (x, y, z) = (cam.TargetX, cam.TargetY, cam.TargetZ);
        cam.PlaceTargetAt(350, 300, 1000, 600);
        var p = cam.Project(x, y, z, 1000, 600);
        Assert.Equal(350, p.X, 4);
        Assert.Equal(300, p.Y, 4);
    }

    [Fact]
    public void NodeSpriteIsOpaqueInTheMiddleAndClearAtTheCorners()
    {
        foreach (var kind in new[] { GraphLayout.Memory, GraphLayout.Session, GraphLayout.File })
        {
            const int size = 64;
            var rgba = NodeSprite.Render(size, kind, (0x30, 0x80, 0xF0), dark: true, staleRing: null);
            Assert.Equal(size * size * 4, rgba.Length);
            var centre = ((size / 2) * size + size / 2) * 4;
            Assert.Equal(255, rgba[centre + 3]);
            Assert.Equal(0, rgba[3]); // top-left corner
            // Glow: semi-transparent just outside the body.
            var glow = ((size / 2) * size + size / 2 + (int)(size * 0.34)) * 4;
            Assert.InRange(rgba[glow + 3], 1, 200);
        }
        Assert.InRange(NodeSprite.ShapeDistance(GraphLayout.Memory, 1, 0), 0.99, 1.01);
        Assert.True(NodeSprite.ShapeDistance(GraphLayout.File, 0.5, 0.5) < 1);
        Assert.True(NodeSprite.ShapeDistance(GraphLayout.Session, 0.95, 0.95) > 1);
    }

    [Fact]
    public void PngEncoderWritesAValidImage()
    {
        var rgba = new byte[3 * 2 * 4];
        for (var i = 0; i < rgba.Length; i++) rgba[i] = (byte)(i * 9);
        var png = Png.Encode(3, 2, rgba);
        Assert.Equal(new byte[] { 0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A }, png[..8]);
        Assert.Equal("IHDR", System.Text.Encoding.ASCII.GetString(png, 12, 4));
        // IDAT inflates back to a filter byte plus the row, twice.
        var idat = IndexOf(png, "IDAT"u8.ToArray());
        var len = (png[idat - 4] << 24) | (png[idat - 3] << 16) | (png[idat - 2] << 8) | png[idat - 1];
        using var z = new System.IO.Compression.ZLibStream(new MemoryStream(png, idat + 4, len), System.IO.Compression.CompressionMode.Decompress);
        using var raw = new MemoryStream();
        z.CopyTo(raw);
        var bytes = raw.ToArray();
        Assert.Equal(2 * (1 + 12), bytes.Length);
        Assert.Equal(rgba[..12], bytes[1..13]);
        Assert.Equal(rgba[12..], bytes[14..]);
        Assert.Equal("IEND", System.Text.Encoding.ASCII.GetString(png, png.Length - 8, 4));
    }

    private static int IndexOf(byte[] data, byte[] needle)
    {
        for (var i = 0; i + needle.Length <= data.Length; i++)
            if (data.AsSpan(i, needle.Length).SequenceEqual(needle)) return i;
        return -1;
    }

    private static double Dist(double[] p, int a, int b)
    {
        var dx = p[a * 3] - p[b * 3];
        var dy = p[a * 3 + 1] - p[b * 3 + 1];
        var dz = p[a * 3 + 2] - p[b * 3 + 2];
        return Math.Sqrt(dx * dx + dy * dy + dz * dz);
    }
}
