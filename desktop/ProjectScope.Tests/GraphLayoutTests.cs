#nullable enable
using Nexus.Core;
using Xunit;

public class GraphLayoutTests
{
    // Two stars (a chat with 12 memories, a memory with 6 files), one pair,
    // and 5 unlinked nodes: the shape the real project graphs have.
    private static (int N, int[] Edges, int[] Kinds, string[] Ids) Sample()
    {
        var kinds = new List<int>();
        var ids = new List<string>();
        var edges = new List<int>();
        int Add(int kind, string id) { kinds.Add(kind); ids.Add(id); return kinds.Count - 1; }
        var chat = Add(GraphLayout.Session, "session:a");
        for (var i = 0; i < 12; i++) { var m = Add(GraphLayout.Memory, $"memory:m{i}"); edges.Add(chat); edges.Add(m); }
        var hub = Add(GraphLayout.Memory, "memory:hub");
        for (var i = 0; i < 6; i++) { var f = Add(GraphLayout.File, $"file:src/f{i}.cs"); edges.Add(hub); edges.Add(f); }
        var p1 = Add(GraphLayout.Memory, "memory:p1");
        var p2 = Add(GraphLayout.Session, "session:p2");
        edges.Add(p1); edges.Add(p2);
        for (var i = 0; i < 5; i++) Add(i % 2 == 0 ? GraphLayout.Session : GraphLayout.File, $"x:iso{i}");
        return (kinds.Count, edges.ToArray(), kinds.ToArray(), ids.ToArray());
    }

    [Fact]
    public void ComponentsAreNumberedBySize()
    {
        var (n, edges, _, _) = Sample();
        var comp = GraphLayout.Components(n, edges, out var count);
        Assert.Equal(3 + 5, count);
        Assert.Equal(0, comp[0]); // the 13-node chat star is the biggest
        Assert.Equal(1, comp[13]); // the 7-node file star next
        Assert.Equal(2, comp[20]);
        Assert.Equal(comp[20], comp[21]);
    }

    [Fact]
    public void LayoutIsDeterministicAndFinite()
    {
        var (n, edges, kinds, ids) = Sample();
        var a = GraphLayout.Layout(n, edges, kinds, ids);
        var b = GraphLayout.Layout(n, edges, kinds, ids);
        Assert.Equal(a, b);
        Assert.All(a, v => Assert.True(double.IsFinite(v)));
    }

    [Fact]
    public void LayoutKeepsNodesApart()
    {
        var (n, edges, kinds, ids) = Sample();
        var pos = GraphLayout.Layout(n, edges, kinds, ids);
        var degree = GraphLayout.Degrees(n, edges);
        for (var i = 0; i < n; i++)
        {
            for (var j = i + 1; j < n; j++)
            {
                var d = Math.Sqrt(Math.Pow(pos[i * 2] - pos[j * 2], 2) + Math.Pow(pos[i * 2 + 1] - pos[j * 2 + 1], 2));
                var min = GraphLayout.NodeRadius(kinds[i], degree[i]) + GraphLayout.NodeRadius(kinds[j], degree[j]);
                Assert.True(d >= min, $"nodes {i} and {j} overlap ({d:0.0} < {min:0.0})");
            }
        }
    }

    [Fact]
    public void LinkedNodesStayCloseAndUnlinkedGoBelow()
    {
        var (n, edges, kinds, ids) = Sample();
        var pos = GraphLayout.Layout(n, edges, kinds, ids);
        // Every leaf of the chat star sits within a few edge lengths of it.
        for (var m = 1; m <= 12; m++)
        {
            var d = Math.Sqrt(Math.Pow(pos[0] - pos[m * 2], 2) + Math.Pow(pos[1] - pos[m * 2 + 1], 2));
            Assert.True(d < GraphLayout.EdgeLength * 3, $"leaf {m} is {d:0} away from its hub");
        }
        // Unlinked nodes are below every linked node.
        var lowestLinked = Enumerable.Range(0, 22).Max(i => pos[i * 2 + 1]);
        for (var i = 22; i < n; i++) Assert.True(pos[i * 2 + 1] > lowestLinked);
    }

    [Fact]
    public void LayoutHandlesEmptySingleAndLargeGraphs()
    {
        Assert.Empty(GraphLayout.Layout(0, [], [], []));
        Assert.Equal(2, GraphLayout.Layout(1, [], [0], ["memory:a"]).Length);

        // 1000 nodes in a few hundred stars must lay out quickly.
        var n = 1000;
        var edges = new List<int>();
        for (var i = 0; i < n; i++) if (i % 8 != 0) { edges.Add(i - i % 8); edges.Add(i); }
        var kinds = Enumerable.Range(0, n).Select(i => i % 3).ToArray();
        var ids = Enumerable.Range(0, n).Select(i => $"n:{i}").ToArray();
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var pos = GraphLayout.Layout(n, edges.ToArray(), kinds, ids);
        Assert.True(watch.ElapsedMilliseconds < 5000, $"layout took {watch.ElapsedMilliseconds} ms");
        Assert.All(pos, v => Assert.True(double.IsFinite(v)));
    }

    [Fact]
    public void LayoutCanBeCancelled()
    {
        var (n, edges, kinds, ids) = Sample();
        using var cts = new CancellationTokenSource();
        cts.Cancel();
        Assert.Throws<OperationCanceledException>(() => GraphLayout.Layout(n, edges, kinds, ids, cts.Token));
    }

    [Fact]
    public void BusierNodesAreBigger()
    {
        Assert.True(GraphLayout.NodeRadius(GraphLayout.Memory, 9) > GraphLayout.NodeRadius(GraphLayout.Memory, 1));
        Assert.Equal(GraphLayout.NodeRadius(GraphLayout.Memory, 400), GraphLayout.NodeRadius(GraphLayout.Memory, 4000)); // capped
    }

    [Fact]
    public void HopsAndRadialFocus()
    {
        var (n, edges, kinds, ids) = Sample();
        // From a chat leaf: the chat is 1 hop, its other memories 2 hops.
        var hops = GraphLayout.Hops(n, edges, 1, 2);
        Assert.Equal(0, hops[1]);
        Assert.Equal(1, hops[0]);
        Assert.Equal(2, hops[5]);
        Assert.Equal(-1, hops[13]);

        var degree = GraphLayout.Degrees(n, edges);
        var radius = Enumerable.Range(0, n).Select(i => GraphLayout.NodeRadius(kinds[i], degree[i])).ToArray();
        var pos = GraphLayout.Radial(n, edges, kinds, ids, radius, 0, 1, out var ring);
        Assert.Equal(0, pos[0]);
        Assert.Equal(0, pos[1]);
        var r1 = Math.Sqrt(pos[2] * pos[2] + pos[3] * pos[3]);
        for (var m = 1; m <= 12; m++)
        {
            Assert.Equal(1, ring[m]);
            Assert.Equal(r1, Math.Sqrt(pos[m * 2] * pos[m * 2] + pos[m * 2 + 1] * pos[m * 2 + 1]), 6);
        }
        Assert.Equal(-1, ring[13]);
    }

    [Fact]
    public void PickLabelsPutsForcedFirstThenBusiest()
    {
        int[] degree = [1, 9, 4, 0, 7];
        var visible = new[] { true, true, true, true, false };
        var picked = GraphLayout.PickLabels(degree, visible, [3, 0], 2);
        Assert.Equal(new[] { 3, 0, 1, 2 }, picked);
        // Hidden nodes never get a label, even when forced.
        Assert.DoesNotContain(4, GraphLayout.PickLabels(degree, visible, [4], 5));
    }

    [Fact]
    public void PlaceLabelsNudgesAndDropsCollisions()
    {
        var slots = GraphLayout.PlaceLabels(
        [
            new(100, 100, 10, 80, 20, false),
            new(100, 100, 10, 80, 20, false), // same spot: moves left
            new(100, 100, 10, 80, 20, false), // moves below
            new(100, 100, 10, 80, 20, false), // moves above
            new(100, 100, 10, 80, 20, false), // nowhere left: dropped
            new(100, 100, 10, 80, 20, true),  // forced: shown anyway
        ]);
        Assert.Equal(114, slots[0].X);
        Assert.True(slots[1].Placed && slots[1].X < 100);
        Assert.True(slots[2].Placed && slots[2].Y > 100);
        Assert.True(slots[3].Placed && slots[3].Y < 100);
        Assert.False(slots[4].Placed);
        Assert.True(slots[5].Placed);
    }

    [Fact]
    public void TruncateAndShortNames()
    {
        Assert.Equal("short", GraphLayout.Truncate("short", 10));
        Assert.Equal("a longer…", GraphLayout.Truncate("a longer title here", 10));
        Assert.True(GraphLayout.Truncate("a longer title here", 10).Length <= 10);
        Assert.Equal("one two", GraphLayout.Truncate("one\ntwo", 20));
        Assert.Equal("GraphPage.cs", GraphLayout.ShortName("file", "desktop/windows/Pages/GraphPage.cs"));
        Assert.Equal("main.go", GraphLayout.ShortName("file", @"cmd\nexus\main.go"));
        Assert.Equal("a/b decision", GraphLayout.ShortName("memory", "a/b decision"));
    }

    [Fact]
    public void SearchRanksExactPrefixWordThenAnywhere()
    {
        string[] labels = ["Graph layout", "graph", "Use the graph", "Paragraphs", "Other"];
        string[] refs = ["", "", "", "", "src/graph.cs"];
        int[] degree = [1, 1, 1, 1, 1];
        Assert.Equal(new[] { 1, 0, 2, 3, 4 }, GraphLayout.Search(labels, refs, degree, "graph", 10));
        Assert.Empty(GraphLayout.Search(labels, refs, degree, "  ", 10));
        Assert.Equal(new[] { 1 }, GraphLayout.Search(labels, refs, degree, "GRAPH", 1));
    }
}
