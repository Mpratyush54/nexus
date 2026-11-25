using System.Numerics;
using Microsoft.UI.Composition;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Hosting;

namespace Nexus.Pages;

/// <summary>
/// Small Composition helpers for the setup guide. Everything checks the
/// Windows "Animation effects" setting (UISettings.AnimationsEnabled): when it
/// is off, elements jump straight to their final state. The other half of
/// this partial class (page transitions, FadeIn, Stagger) is in NexusUi.cs.
/// </summary>
internal static partial class Motion
{
    // Enabled (the Windows "Animation effects" setting) lives with the rest
    // of Motion in NexusUi.cs.

    private static Visual VisualOf(UIElement element) => ElementCompositionPreview.GetElementVisual(element);

    private static CompositionEasingFunction EaseOut(Compositor c) =>
        c.CreateCubicBezierEasingFunction(new Vector2(0.1f, 0.9f), new Vector2(0.2f, 1f));

    // Keeps scale / rotation centred on the element whatever its size.
    private static void Centre(Visual visual, float x = 0.5f, float y = 0.5f)
    {
        var centre = visual.Compositor.CreateExpressionAnimation($"Vector3(this.Target.Size.X * {x}, this.Target.Size.Y * {y}, 0)");
        visual.StartAnimation("CenterPoint", centre);
    }

    /// <summary>Fade in while sliding by (dx, dy) to rest.</summary>
    public static void FadeSlideIn(UIElement element, int delayMs = 0, float dx = 0, float dy = 12, int durationMs = 420)
    {
        var visual = VisualOf(element);
        if (!Enabled)
        {
            visual.Opacity = 1;
            return;
        }
        var c = visual.Compositor;
        ElementCompositionPreview.SetIsTranslationEnabled(element, true);
        visual.Opacity = 0;
        var fade = c.CreateScalarKeyFrameAnimation();
        fade.InsertKeyFrame(0f, 0f);
        fade.InsertKeyFrame(1f, 1f, EaseOut(c));
        fade.Duration = TimeSpan.FromMilliseconds(durationMs);
        fade.DelayTime = TimeSpan.FromMilliseconds(delayMs);
        fade.DelayBehavior = AnimationDelayBehavior.SetInitialValueBeforeDelay;
        var slide = c.CreateVector3KeyFrameAnimation();
        slide.InsertKeyFrame(0f, new Vector3(dx, dy, 0));
        slide.InsertKeyFrame(1f, Vector3.Zero, EaseOut(c));
        slide.Duration = fade.Duration;
        slide.DelayTime = fade.DelayTime;
        slide.DelayBehavior = AnimationDelayBehavior.SetInitialValueBeforeDelay;
        visual.StartAnimation("Opacity", fade);
        visual.StartAnimation("Translation", slide);
    }

    /// <summary>Scale up from <paramref name="from"/> with a little overshoot.</summary>
    public static void PopIn(UIElement element, int delayMs = 0, float from = 0.4f, int durationMs = 520)
    {
        var visual = VisualOf(element);
        if (!Enabled)
        {
            visual.Opacity = 1;
            visual.Scale = Vector3.One;
            return;
        }
        var c = visual.Compositor;
        Centre(visual);
        visual.Opacity = 0;
        var overshoot = c.CreateCubicBezierEasingFunction(new Vector2(0.34f, 1.56f), new Vector2(0.64f, 1f));
        var scale = c.CreateVector3KeyFrameAnimation();
        scale.InsertKeyFrame(0f, new Vector3(from, from, 1));
        scale.InsertKeyFrame(1f, Vector3.One, overshoot);
        scale.Duration = TimeSpan.FromMilliseconds(durationMs);
        scale.DelayTime = TimeSpan.FromMilliseconds(delayMs);
        scale.DelayBehavior = AnimationDelayBehavior.SetInitialValueBeforeDelay;
        var fade = c.CreateScalarKeyFrameAnimation();
        fade.InsertKeyFrame(0f, 0f);
        fade.InsertKeyFrame(1f, 1f);
        fade.Duration = TimeSpan.FromMilliseconds(durationMs * 0.6);
        fade.DelayTime = scale.DelayTime;
        fade.DelayBehavior = AnimationDelayBehavior.SetInitialValueBeforeDelay;
        visual.StartAnimation("Scale", scale);
        visual.StartAnimation("Opacity", fade);
    }

    /// <summary>Slow, endless breathing (scale) for a resting logo.</summary>
    public static void Breathe(UIElement element, float amount = 0.035f, int periodMs = 2800, int delayMs = 600)
    {
        if (!Enabled) return;
        var visual = VisualOf(element);
        var c = visual.Compositor;
        Centre(visual);
        var ease = c.CreateCubicBezierEasingFunction(new Vector2(0.45f, 0f), new Vector2(0.55f, 1f));
        var breathe = c.CreateVector3KeyFrameAnimation();
        breathe.InsertKeyFrame(0f, Vector3.One);
        breathe.InsertKeyFrame(0.5f, new Vector3(1 + amount, 1 + amount, 1), ease);
        breathe.InsertKeyFrame(1f, Vector3.One, ease);
        breathe.Duration = TimeSpan.FromMilliseconds(periodMs);
        breathe.DelayTime = TimeSpan.FromMilliseconds(delayMs);
        breathe.IterationBehavior = AnimationIterationBehavior.Forever;
        visual.StartAnimation("Scale", breathe);
    }

    /// <summary>Endless slow rotation (the orbiting memory-graph ring).</summary>
    public static void Spin(UIElement element, int periodMs = 30000)
    {
        if (!Enabled) return;
        var visual = VisualOf(element);
        var c = visual.Compositor;
        Centre(visual);
        var spin = c.CreateScalarKeyFrameAnimation();
        spin.InsertKeyFrame(0f, 0f);
        spin.InsertKeyFrame(1f, 360f, c.CreateLinearEasingFunction());
        spin.Duration = TimeSpan.FromMilliseconds(periodMs);
        spin.IterationBehavior = AnimationIterationBehavior.Forever;
        visual.StartAnimation("RotationAngleInDegrees", spin);
    }

    /// <summary>Endless opacity shimmer between <paramref name="low"/> and 1.</summary>
    public static void Twinkle(UIElement element, int periodMs, int delayMs, float low = 0.35f)
    {
        if (!Enabled) return;
        var visual = VisualOf(element);
        var c = visual.Compositor;
        var tw = c.CreateScalarKeyFrameAnimation();
        tw.InsertKeyFrame(0f, 1f);
        tw.InsertKeyFrame(0.5f, low);
        tw.InsertKeyFrame(1f, 1f);
        tw.Duration = TimeSpan.FromMilliseconds(periodMs);
        tw.DelayTime = TimeSpan.FromMilliseconds(delayMs);
        tw.IterationBehavior = AnimationIterationBehavior.Forever;
        visual.StartAnimation("Opacity", tw);
    }

    /// <summary>Grow a bar's horizontal scale from <paramref name="from"/> to <paramref name="to"/> (left anchored).</summary>
    public static void FillX(UIElement element, float from, float to, int durationMs = 480)
    {
        var visual = VisualOf(element);
        Centre(visual, 0f, 0.5f);
        if (!Enabled || Math.Abs(from - to) < 0.001f)
        {
            visual.Scale = new Vector3(to, 1, 1);
            return;
        }
        var c = visual.Compositor;
        var fill = c.CreateVector3KeyFrameAnimation();
        fill.InsertKeyFrame(0f, new Vector3(from, 1, 1));
        fill.InsertKeyFrame(1f, new Vector3(to, 1, 1), EaseOut(c));
        fill.Duration = TimeSpan.FromMilliseconds(durationMs);
        visual.StartAnimation("Scale", fill);
    }

    /// <summary>Fade (and slightly shrink) out; completes when the animation ends.</summary>
    public static Task FadeOutAsync(UIElement element, int durationMs = 260)
    {
        var visual = VisualOf(element);
        if (!Enabled) return Task.CompletedTask;
        var c = visual.Compositor;
        Centre(visual);
        var done = new TaskCompletionSource();
        var batch = c.CreateScopedBatch(CompositionBatchTypes.Animation);
        var fade = c.CreateScalarKeyFrameAnimation();
        fade.InsertKeyFrame(1f, 0f);
        fade.Duration = TimeSpan.FromMilliseconds(durationMs);
        var scale = c.CreateVector3KeyFrameAnimation();
        scale.InsertKeyFrame(1f, new Vector3(0.98f, 0.98f, 1));
        scale.Duration = fade.Duration;
        visual.StartAnimation("Opacity", fade);
        visual.StartAnimation("Scale", scale);
        batch.End();
        batch.Completed += (_, _) => done.TrySetResult();
        return done.Task;
    }
}
