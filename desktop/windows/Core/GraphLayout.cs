#nullable enable
namespace Nexus.Core;

/// <summary>
/// Pure layout math for the memory graph (no UI types, so it is testable and
/// runs off the UI thread). Nodes are indexes 0..n-1; edges are a flat array
/// of index pairs [a0, b0, a1, b1, ...]. Positions come back as [x0, y0, ...].
/// </summary>
internal static class GraphLayout
{
    /// <summary>Kind codes, also the order kinds are grouped in.</summary>
    public const int Memory = 0, Session = 1, File = 2;

    public static int KindCode(string kind) => kind switch
    {
        "session" => Session,
        "file" => File,
        _ => Memory
    };

    /// <summary>Ideal edge length in world units.</summary>
    public const double EdgeLength = 70;
    private const double ComponentGap = 56;
    private const double IsolateCell = 34;

    public static uint StableHash(string s)
    {
        uint h = 2166136261;
        foreach (var c in s)
        {
            h ^= c;
            h *= 16777619;
        }
        return h;
    }

    public static int[] Degrees(int n, int[] edges)
    {
        var degree = new int[n];
        for (var e = 0; e + 1 < edges.Length; e += 2)
        {
            degree[edges[e]]++;
            degree[edges[e + 1]]++;
        }
        return degree;
    }

    /// <summary>Node radius in world units: bigger for busier nodes, capped.</summary>
    public static double NodeRadius(int kind, int degree)
    {
        var baseR = kind switch { Session => 9.0, File => 7.5, _ => 8.0 };
        return baseR + Math.Min(16, Math.Sqrt(Math.Max(0, degree)) * 3.2);
    }

    public static List<int>[] Adjacency(int n, int[] edges)
    {
        var adj = new List<int>[n];
        for (var i = 0; i < n; i++) adj[i] = new List<int>();
        for (var e = 0; e + 1 < edges.Length; e += 2)
        {
            int a = edges[e], b = edges[e + 1];
            if (a == b) continue;
            adj[a].Add(b);
            adj[b].Add(a);
        }
        return adj;
    }

    /// <summary>
    /// Connected components: a component id per node. Ids are numbered by
    /// size, largest first (ties broken by the smallest member index).
    /// </summary>
    public static int[] Components(int n, int[] edges, out int count)
    {
        var adj = Adjacency(n, edges);
        var raw = new int[n];
        Array.Fill(raw, -1);
        var sizes = new List<(int Size, int First)>();
        var stack = new Stack<int>();
        for (var i = 0; i < n; i++)
        {
            if (raw[i] >= 0) continue;
            var id = sizes.Count;
            var size = 0;
            raw[i] = id;
            stack.Push(i);
            while (stack.Count > 0)
            {
                var v = stack.Pop();
                size++;
                foreach (var w in adj[v])
                {
                    if (raw[w] >= 0) continue;
                    raw[w] = id;
                    stack.Push(w);
                }
            }
            sizes.Add((size, i));
        }
        count = sizes.Count;
        var order = Enumerable.Range(0, count).OrderByDescending(c => sizes[c].Size).ThenBy(c => sizes[c].First).ToArray();
        var rank = new int[count];
        for (var r = 0; r < count; r++) rank[order[r]] = r;
        var result = new int[n];
        for (var i = 0; i < n; i++) result[i] = rank[raw[i]];
        return result;
    }

    /// <summary>Hop distance from start (−1 when farther than maxHops or unreachable).</summary>
    public static int[] Hops(int n, int[] edges, int start, int maxHops)
    {
        var hops = new int[n];
        Array.Fill(hops, -1);
        if (start < 0 || start >= n) return hops;
        var adj = Adjacency(n, edges);
        var queue = new Queue<int>();
        hops[start] = 0;
        queue.Enqueue(start);
        while (queue.Count > 0)
        {
            var v = queue.Dequeue();
            if (hops[v] >= maxHops) continue;
            foreach (var w in adj[v])
            {
                if (hops[w] >= 0) continue;
                hops[w] = hops[v] + 1;
                queue.Enqueue(w);
            }
        }
        return hops;
    }

    /// <summary>
    /// The whole-graph layout. Each connected component is laid out on its
    /// own (seeded rings around its busiest node, then a force-directed pass
    /// with overlap removal), components are packed in rows, biggest first,
    /// and unlinked nodes go in a tidy grid below, grouped by kind. Same
    /// input, same picture.
    /// </summary>
    public static double[] Layout(int n, int[] edges, int[] kinds, string[] ids, CancellationToken ct = default)
    {
        var pos = new double[n * 2];
        if (n == 0) return pos;
        var degree = Degrees(n, edges);
        var radius = new double[n];
        for (var i = 0; i < n; i++) radius[i] = NodeRadius(kinds[i], degree[i]);
        var hash = new uint[n];
        for (var i = 0; i < n; i++) hash[i] = StableHash(ids[i]);
        var comp = Components(n, edges, out var count);
        var adj = Adjacency(n, edges);

        var members = new List<int>[count];
        for (var c = 0; c < count; c++) members[c] = new List<int>();
        for (var i = 0; i < n; i++) members[comp[i]].Add(i);

        // Lay out each linked component around its own origin.
        var boxes = new List<(int Comp, double MinX, double MinY, double W, double H)>();
        var isolates = new List<int>();
        for (var c = 0; c < count; c++)
        {
            ct.ThrowIfCancellationRequested();
            var m = members[c];
            if (m.Count == 1)
            {
                isolates.Add(m[0]);
                continue;
            }
            LayoutComponent(m, adj, kinds, degree, radius, hash, pos, ct);
            double minX = double.MaxValue, minY = double.MaxValue, maxX = double.MinValue, maxY = double.MinValue;
            foreach (var i in m)
            {
                minX = Math.Min(minX, pos[i * 2] - radius[i]);
                maxX = Math.Max(maxX, pos[i * 2] + radius[i]);
                minY = Math.Min(minY, pos[i * 2 + 1] - radius[i]);
                maxY = Math.Max(maxY, pos[i * 2 + 1] + radius[i]);
            }
            boxes.Add((c, minX, minY, maxX - minX, maxY - minY));
        }

        // Shelf-pack component boxes into rows about as wide as they are tall.
        var area = boxes.Sum(b => (b.W + ComponentGap) * (b.H + ComponentGap)) + isolates.Count * IsolateCell * IsolateCell;
        var rowWidth = Math.Max(Math.Sqrt(Math.Max(1, area)) * 1.25, boxes.Count > 0 ? boxes.Max(b => b.W) : 0);
        rowWidth = Math.Max(rowWidth, IsolateCell * 8);
        double x = 0, y = 0, rowH = 0;
        foreach (var b in boxes)
        {
            if (x > 0 && x + b.W > rowWidth)
            {
                x = 0;
                y += rowH + ComponentGap;
                rowH = 0;
            }
            var dx = x - b.MinX;
            var dy = y - b.MinY;
            foreach (var i in members[b.Comp])
            {
                pos[i * 2] += dx;
                pos[i * 2 + 1] += dy;
            }
            x += b.W + ComponentGap;
            rowH = Math.Max(rowH, b.H);
        }
        // Unlinked nodes: one grid block per kind under the components.
        var top = boxes.Count == 0 ? 0 : y + rowH + ComponentGap * 1.5;
        var cols = Math.Max(6, (int)(rowWidth / IsolateCell));
        foreach (var kind in new[] { Memory, Session, File })
        {
            var group = isolates.Where(i => kinds[i] == kind).OrderBy(i => hash[i]).ThenBy(i => i).ToList();
            if (group.Count == 0) continue;
            for (var k = 0; k < group.Count; k++)
            {
                var i = group[k];
                pos[i * 2] = (k % cols) * IsolateCell + IsolateCell / 2;
                pos[i * 2 + 1] = top + (k / cols) * IsolateCell + IsolateCell / 2;
            }
            top += ((group.Count - 1) / cols + 1) * IsolateCell + ComponentGap * 0.75;
        }
        return pos;
    }

    private static void LayoutComponent(List<int> m, List<int>[] adj, int[] kinds, int[] degree, double[] radius, uint[] hash, double[] pos, CancellationToken ct)
    {
        var count = m.Count;
        var local = new Dictionary<int, int>(count);
        for (var k = 0; k < count; k++) local[m[k]] = k;

        // Seed: rings by hop distance from the busiest node; hop-1 nodes
        // spread evenly (grouped by kind), deeper nodes near their parent.
        var hub = m.OrderByDescending(i => degree[i]).ThenBy(i => hash[i]).ThenBy(i => i).First();
        var angle = new double[count];
        var hop = new int[count];
        Array.Fill(hop, -1);
        var p = new double[count * 2];
        hop[local[hub]] = 0;
        var frontier = new List<int> { hub };
        var level = 0;
        while (frontier.Count > 0)
        {
            var next = new List<(int Node, int Parent)>();
            foreach (var v in frontier)
            {
                foreach (var w in adj[v])
                {
                    var lw = local[w];
                    if (hop[lw] >= 0) continue;
                    hop[lw] = level + 1;
                    next.Add((w, v));
                }
            }
            level++;
            if (next.Count == 0) break;
            if (level == 1)
            {
                var ordered = next.OrderBy(t => kinds[t.Node]).ThenBy(t => hash[t.Node]).ToList();
                for (var k = 0; k < ordered.Count; k++) angle[local[ordered[k].Node]] = 2 * Math.PI * k / ordered.Count;
            }
            else
            {
                var bySibling = next.GroupBy(t => t.Parent);
                foreach (var g in bySibling)
                {
                    var sibs = g.OrderBy(t => kinds[t.Node]).ThenBy(t => hash[t.Node]).ToList();
                    var spread = Math.Min(Math.PI / 2, 0.35 * sibs.Count) / level;
                    for (var k = 0; k < sibs.Count; k++)
                    {
                        var offset = sibs.Count == 1 ? 0 : (k / (double)(sibs.Count - 1) - 0.5) * spread;
                        angle[local[sibs[k].Node]] = angle[local[g.Key]] + offset;
                    }
                }
            }
            frontier = next.Select(t => t.Node).ToList();
        }
        for (var k = 0; k < count; k++)
        {
            var r = Math.Max(0, hop[k]) * EdgeLength;
            p[k * 2] = Math.Cos(angle[k]) * r;
            p[k * 2 + 1] = Math.Sin(angle[k]) * r;
        }

        // Local edge list.
        var le = new List<int>();
        foreach (var v in m)
        {
            foreach (var w in adj[v])
            {
                if (v < w)
                {
                    le.Add(local[v]);
                    le.Add(local[w]);
                }
            }
        }
        var edges = le.ToArray();
        var rad = new double[count];
        for (var k = 0; k < count; k++) rad[k] = radius[m[k]];

        ForceDirected(count, edges, rad, p, ct);
        RemoveOverlaps(count, rad, p, 10, 30);

        for (var k = 0; k < count; k++)
        {
            pos[m[k] * 2] = p[k * 2];
            pos[m[k] * 2 + 1] = p[k * 2 + 1];
        }
    }

    /// <summary>
    /// Fruchterman-Reingold on one component: k²/d repulsion between all
    /// pairs (spaced for node size), d²/k springs along edges, a weak pull to
    /// the centre and a cooling step cap. Starts from the seeded rings, so it
    /// converges in few iterations.
    /// </summary>
    public static void ForceDirected(int n, int[] edges, double[] radius, double[] p, CancellationToken ct = default)
    {
        if (n <= 1) return;
        var iterations = n <= 60 ? 220 : n <= 300 ? 160 : 100;
        var disp = new double[n * 2];
        var k = EdgeLength;
        var k2 = k * k;
        var temp = k * 1.5;
        for (var iter = 0; iter < iterations; iter++)
        {
            if ((iter & 15) == 0) ct.ThrowIfCancellationRequested();
            Array.Clear(disp);
            for (var i = 0; i < n; i++)
            {
                var xi = p[i * 2];
                var yi = p[i * 2 + 1];
                for (var j = i + 1; j < n; j++)
                {
                    var dx = xi - p[j * 2];
                    var dy = yi - p[j * 2 + 1];
                    var d2 = dx * dx + dy * dy;
                    if (d2 < 0.01)
                    {
                        dx = 0.1 * ((i * 7 + j) % 5 - 2) + 0.05;
                        dy = 0.1 * ((i + j * 3) % 5 - 2) + 0.05;
                        d2 = dx * dx + dy * dy;
                    }
                    if (d2 > 36 * k2) continue; // far apart: negligible
                    // Larger nodes push harder, so hubs get room for their leaves.
                    var scale = (radius[i] + radius[j]) / 16.0;
                    var f = k2 * scale / d2;
                    disp[i * 2] += dx * f;
                    disp[i * 2 + 1] += dy * f;
                    disp[j * 2] -= dx * f;
                    disp[j * 2 + 1] -= dy * f;
                }
            }
            for (var e = 0; e + 1 < edges.Length; e += 2)
            {
                var a = edges[e];
                var b = edges[e + 1];
                var dx = p[a * 2] - p[b * 2];
                var dy = p[a * 2 + 1] - p[b * 2 + 1];
                var d = Math.Sqrt(dx * dx + dy * dy) + 0.01;
                var f = d / k;
                disp[a * 2] -= dx * f;
                disp[a * 2 + 1] -= dy * f;
                disp[b * 2] += dx * f;
                disp[b * 2 + 1] += dy * f;
            }
            for (var i = 0; i < n; i++)
            {
                var x = p[i * 2];
                var y = p[i * 2 + 1];
                var dx = disp[i * 2] - x * 0.02;
                var dy = disp[i * 2 + 1] - y * 0.02;
                var len = Math.Sqrt(dx * dx + dy * dy);
                if (len > 0)
                {
                    var step = Math.Min(len, temp);
                    p[i * 2] = x + dx / len * step;
                    p[i * 2 + 1] = y + dy / len * step;
                }
            }
            temp = Math.Max(0.5, temp * 0.96);
        }
    }

    /// <summary>Pushes apart nodes closer than their radii plus a gap.</summary>
    public static void RemoveOverlaps(int n, double[] radius, double[] p, double gap, int passes)
    {
        for (var pass = 0; pass < passes; pass++)
        {
            var moved = false;
            for (var i = 0; i < n; i++)
            {
                for (var j = i + 1; j < n; j++)
                {
                    var dx = p[j * 2] - p[i * 2];
                    var dy = p[j * 2 + 1] - p[i * 2 + 1];
                    var min = radius[i] + radius[j] + gap;
                    var d2 = dx * dx + dy * dy;
                    if (d2 >= min * min) continue;
                    var d = Math.Sqrt(d2);
                    if (d < 0.001)
                    {
                        dx = (i % 2 == 0 ? 1 : -1) * 0.7;
                        dy = (j % 2 == 0 ? 1 : -1) * 0.7;
                        d = 1;
                    }
                    var push = (min - d) / 2 / d;
                    p[i * 2] -= dx * push;
                    p[i * 2 + 1] -= dy * push;
                    p[j * 2] += dx * push;
                    p[j * 2 + 1] += dy * push;
                    moved = true;
                }
            }
            if (!moved) return;
        }
    }

    /// <summary>
    /// Focus mode: the centre node in the middle, its neighbours on a ring
    /// (grouped by kind) and their neighbours on an outer ring next to their
    /// parent. Returns positions and the hop of each node (−1 = not shown).
    /// </summary>
    public static double[] Radial(int n, int[] edges, int[] kinds, string[] ids, double[] radius, int center, int maxHops, out int[] hops)
    {
        hops = Hops(n, edges, center, maxHops);
        var pos = new double[n * 2];
        if (center < 0 || center >= n) return pos;
        var adj = Adjacency(n, edges);
        var hash = ids.Select(StableHash).ToArray();
        var angle = new double[n];
        var prevRadius = 0.0;
        var maxR = 0.0;
        for (var i = 0; i < n; i++) if (hops[i] >= 0) maxR = Math.Max(maxR, radius[i]);
        var spacing = 2 * maxR + 22;
        for (var h = 1; h <= maxHops; h++)
        {
            List<int> ring;
            var hh = h;
            var localHops = hops;
            if (h == 1)
            {
                ring = Enumerable.Range(0, n).Where(i => localHops[i] == 1).OrderBy(i => kinds[i]).ThenBy(i => hash[i]).ThenBy(i => i).ToList();
            }
            else
            {
                // Parent = the inner neighbour with the smallest angle; keep
                // each parent's children together, in parent-angle order.
                var parentAngle = new Dictionary<int, double>();
                foreach (var i in Enumerable.Range(0, n).Where(i => localHops[i] == hh))
                {
                    var best = double.MaxValue;
                    foreach (var w in adj[i]) if (localHops[w] == hh - 1) best = Math.Min(best, angle[w]);
                    parentAngle[i] = best;
                }
                ring = parentAngle.Keys.OrderBy(i => parentAngle[i]).ThenBy(i => kinds[i]).ThenBy(i => hash[i]).ThenBy(i => i).ToList();
            }
            if (ring.Count == 0) break;
            var r = Math.Max(prevRadius + 150, ring.Count * spacing / (2 * Math.PI));
            if (h == 1) r = Math.Max(r, 150);
            // Both rings run clockwise from the top, and the outer ring is
            // sorted by parent angle, so children sit roughly by their parent.
            var half = h == 1 ? 0 : 0.5;
            for (var k = 0; k < ring.Count; k++)
            {
                var i = ring[k];
                angle[i] = 2 * Math.PI * (k + half) / ring.Count - Math.PI / 2;
                pos[i * 2] = Math.Cos(angle[i]) * r;
                pos[i * 2 + 1] = Math.Sin(angle[i]) * r;
            }
            prevRadius = r;
        }
        return pos;
    }

    // ---- labels ----

    /// <summary>Shortens text to max characters with an ellipsis.</summary>
    public static string Truncate(string text, int max)
    {
        text = (text ?? "").Trim().Replace('\n', ' ').Replace('\r', ' ');
        if (max <= 1) return text.Length > 0 ? "…" : "";
        return text.Length <= max ? text : text[..(max - 1)].TrimEnd() + "…";
    }

    private static readonly System.Text.RegularExpressions.Regex NoticeHeader = new(
        @"^\s*\[\s*(?:SYSTEM|TASK|AGENT|BACKGROUND|SUBAGENT|HOOK|AUTOMATED)\b[^\]\n]*(?:\]|$)",
        System.Text.RegularExpressions.RegexOptions.IgnoreCase | System.Text.RegularExpressions.RegexOptions.CultureInvariant);

    /// <summary>
    /// Drops leading harness notices ("[SYSTEM NOTIFICATION - NOT USER INPUT]",
    /// "[TASK NOTIFICATION] …"), closed or cut off, and blank lines, keeping
    /// the first real line onwards. Returns "" when nothing real is left.
    /// </summary>
    public static string StripNotices(string? text)
    {
        var lines = (text ?? "").Replace("\r\n", "\n").Split('\n');
        for (var i = 0; i < lines.Length; i++)
        {
            var line = lines[i];
            while (line.Length > 0 && NoticeHeader.Match(line) is { Success: true, Length: > 0 } m) line = line[m.Length..];
            if (line.Trim().Length == 0) continue;
            lines[i] = line;
            return string.Join("\n", lines[i..]).Trim();
        }
        return "";
    }

    /// <summary>A short name for a node: a file's base name, others as is.</summary>
    public static string ShortName(string kind, string label)
    {
        if (kind != "file") return label;
        var trimmed = label.TrimEnd('/', '\\');
        var slash = Math.Max(trimmed.LastIndexOf('/'), trimmed.LastIndexOf('\\'));
        return slash >= 0 && slash < trimmed.Length - 1 ? trimmed[(slash + 1)..] : trimmed;
    }

    /// <summary>Rough label width for a UI font of the given size.</summary>
    public static double EstimateWidth(string text, double fontSize) => text.Length * fontSize * 0.56 + 10;

    /// <summary>
    /// The nodes that get a permanent label: forced ones (selection, its
    /// neighbours, the hovered node) first, then the busiest nodes up to
    /// budget. Only candidates for which visible[i] is true are considered.
    /// </summary>
    public static List<int> PickLabels(int[] degree, bool[] visible, IReadOnlyCollection<int> forced, int budget)
    {
        var result = new List<int>();
        var seen = new HashSet<int>();
        foreach (var f in forced)
        {
            if (f >= 0 && f < degree.Length && visible[f] && seen.Add(f)) result.Add(f);
        }
        var ranked = Enumerable.Range(0, degree.Length)
            .Where(i => visible[i] && degree[i] > 0 && !seen.Contains(i))
            .OrderByDescending(i => degree[i]).ThenBy(i => i)
            .Take(Math.Max(0, budget));
        result.AddRange(ranked);
        return result;
    }

    public readonly record struct LabelRequest(double X, double Y, double Radius, double Width, double Height, bool Forced);

    public readonly record struct LabelSlot(double X, double Y, bool Placed);

    /// <summary>
    /// Greedy label placement in priority order: try right, left, below and
    /// above the node; skip a label when every spot collides with an earlier
    /// one, unless it is forced (then it goes to the right regardless).
    /// With a viewport (viewW, viewH &gt; 0), spots that would cross its edges
    /// are skipped, and a forced label is pushed fully inside, so no label is
    /// clipped at the edge of the view.
    /// </summary>
    public static LabelSlot[] PlaceLabels(IReadOnlyList<LabelRequest> requests, double viewW = 0, double viewH = 0)
    {
        const double margin = 4;
        var bounded = viewW > 0 && viewH > 0;
        bool Inside(double x, double y, double w, double h) =>
            !bounded || (x >= margin && y >= margin && x + w <= viewW - margin && y + h <= viewH - margin);
        var slots = new LabelSlot[requests.Count];
        var taken = new List<(double X, double Y, double W, double H)>();
        for (var k = 0; k < requests.Count; k++)
        {
            var r = requests[k];
            var gap = 4.0;
            var candidates = new (double X, double Y)[]
            {
                (r.X + r.Radius + gap, r.Y - r.Height / 2),
                (r.X - r.Radius - gap - r.Width, r.Y - r.Height / 2),
                (r.X - r.Width / 2, r.Y + r.Radius + 2),
                (r.X - r.Width / 2, r.Y - r.Radius - 2 - r.Height),
            };
            var placed = false;
            foreach (var c in candidates)
            {
                if (!Inside(c.X, c.Y, r.Width, r.Height) || Collides(taken, c.X, c.Y, r.Width, r.Height)) continue;
                slots[k] = new LabelSlot(c.X, c.Y, true);
                taken.Add((c.X, c.Y, r.Width, r.Height));
                placed = true;
                break;
            }
            if (!placed && r.Forced)
            {
                var (x, y) = candidates[0];
                if (bounded)
                {
                    // Right side unless the left has more room, then clamped inside.
                    if (x + r.Width > viewW - margin && candidates[1].X >= margin) x = candidates[1].X;
                    x = Math.Max(margin, Math.Min(x, viewW - margin - r.Width));
                    y = Math.Max(margin, Math.Min(y, viewH - margin - r.Height));
                }
                slots[k] = new LabelSlot(x, y, true);
                taken.Add((x, y, r.Width, r.Height));
            }
        }
        return slots;
    }

    private static bool Collides(List<(double X, double Y, double W, double H)> taken, double x, double y, double w, double h)
    {
        foreach (var t in taken)
        {
            if (x < t.X + t.W && t.X < x + w && y < t.Y + t.H && t.Y < y + h) return true;
        }
        return false;
    }

    // ---- search ----

    /// <summary>
    /// Nodes whose label (or ref) matches the query: exact, then prefix, then
    /// word start, then anywhere; busier nodes first within a rank.
    /// </summary>
    public static List<int> Search(IReadOnlyList<string> labels, IReadOnlyList<string> refs, int[] degree, string query, int max)
    {
        var q = (query ?? "").Trim();
        if (q.Length == 0) return new List<int>();
        var hits = new List<(int Index, int Rank)>();
        for (var i = 0; i < labels.Count; i++)
        {
            var rank = Rank(labels[i], q);
            if (rank < 0 && i < refs.Count) rank = Rank(refs[i], q) is var rr and >= 0 ? rr + 4 : -1;
            if (rank >= 0) hits.Add((i, rank));
        }
        return hits.OrderBy(h => h.Rank).ThenByDescending(h => degree[h.Index]).ThenBy(h => labels[h.Index], StringComparer.OrdinalIgnoreCase)
            .Take(max).Select(h => h.Index).ToList();
    }

    private static int Rank(string text, string q)
    {
        if (string.IsNullOrEmpty(text)) return -1;
        if (text.Equals(q, StringComparison.OrdinalIgnoreCase)) return 0;
        if (text.StartsWith(q, StringComparison.OrdinalIgnoreCase)) return 1;
        var at = text.IndexOf(q, StringComparison.OrdinalIgnoreCase);
        if (at < 0) return -1;
        var before = text[at - 1];
        return char.IsLetterOrDigit(before) ? 3 : 2;
    }
}
