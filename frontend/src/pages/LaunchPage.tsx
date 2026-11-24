import {
  ArrowDown,
  ArrowRight,
  Check,
  Download,
  GitFork,
  Monitor,
  ShieldCheck,
} from "lucide-react";
import { Link } from "react-router-dom";
import { NexusBrand } from "@/components/NexusBrand";
import { ProductDemo } from "@/components/ProductDemo";
import { PlanGrid } from "@/components/PlanGrid";
import { useToast } from "@/components/ui/Toast";
import { useBillingPlans } from "@/hooks/useBilling";
import { useAuth } from "@/providers/AuthProvider";
import "@/landing.css";

export function LaunchPage() {
  const { isAuthenticated } = useAuth(),
    plans = useBillingPlans(),
    { push } = useToast();
  const app = isAuthenticated ? "/app/dashboard" : "/signup";
  // Configure only after the new film has been rendered and verified.
  const film = import.meta.env.VITE_NEXUS_LAUNCH_FILM_URL as string | undefined;
  return (
    <div className="launch-site">
      <header className="launch-nav">
        <Link to="/" aria-label="Nexus home">
          <NexusBrand />
        </Link>
        <nav aria-label="Main navigation">
          <a href="#product">Product</a>
          <a href="#teams">For teams</a>
          <a href="#privacy">Privacy</a>
          <Link to={isAuthenticated ? "/app/dashboard" : "/login"}>
            {isAuthenticated ? "Open workspace" : "Log in"}
            <ArrowRight size={14} />
          </Link>
        </nav>
      </header>
      <main>
        <section className="launch-hero">
          <div className="launch-announcement">
            <span />
            Windows beta · macOS coming soon
          </div>
          <h1>
            Your agent changes.
            <br />
            <span>Your context doesn’t.</span>
          </h1>
          <div className="launch-hero-bottom">
            <p>
              Move from one AI agent to another without rebuilding the story.
              Keep the conversation, recorded changes and decisions together—then
              hand the work to a teammate, not just a transcript.
            </p>
            <div className="launch-hero-ctas">
              <a className="launch-primary" href="#beta">
                <Download size={16} />
                Get the desktop beta
              </a>
              <Link className="launch-secondary" to={app}>
                {isAuthenticated ? "Open Nexus Web" : "Create your workspace"}
                <ArrowRight size={15} />
              </Link>
            </div>
          </div>
          <div className="launch-agent-line">
            <span>Keep your tools.</span>
            <strong>Cursor</strong>
            <strong>Codex</strong>
            <strong>OpenCode</strong>
            <strong>Claude Code</strong>
            <a href="#product" aria-label="Explore the product">
              <ArrowDown size={17} />
            </a>
          </div>
        </section>
        <section
          id="product"
          className="launch-proof"
          aria-label="Product demonstration"
        >
          <div className="launch-section-intro">
            <span>Context that travels with the work</span>
            <p>A different agent. A different teammate. Not a fresh start.</p>
          </div>
          <ProductDemo />
          {film && (
            <div className="launch-film">
              <video
                controls
                playsInline
                preload="none"
                aria-label="Nexus launch film"
                src={film}
              />
              <p>From scattered to shared · 60 seconds · sound on</p>
            </div>
          )}
        </section>
        <section className="launch-continuity">
          <div className="launch-editorial">
            <span>Nothing important left behind</span>
            <h2>
              Change your agent.
              <br />
              Bring your context.
              <br />
              <em>Keep going.</em>
            </h2>
          </div>
          <div className="launch-explanations">
            <article>
              <h3>Continue the work, not the setup.</h3>
              <p>
                Find a project conversation and continue with another supported,
                installed agent. Nexus carries captured context into the next
                turn, so the reasoning doesn’t stay trapped in the last tool.
              </p>
            </article>
            <article>
              <h3>The decision and the change.</h3>
              <p>
                Expand recorded tool activity and inspect saved patches with
                added and removed lines. Older captures without a patch are
                labelled honestly.
              </p>
            </article>
            <article>
              <h3>A handoff with something to pick up.</h3>
              <p>
                Teleport a conversation with a verified code package. Your
                teammate reviews the context and restores a separate copy,
                without overwriting their existing workspace.
              </p>
            </article>
          </div>
        </section>
        <section id="teams" className="launch-teams">
          <div className="launch-team-heading">
            <span>Built to be picked up</span>
            <h2>
              One person’s progress.
              <br />
              <span>The whole team’s context.</span>
            </h2>
            <p>
              Don’t ask a teammate to reconstruct your work from a pasted reply.
              Send the conversation and code together. They can review what was
              decided, inspect the saved changes, and pick up the next step.
            </p>
          </div>
          <div className="launch-handoff">
            <div className="launch-handoff-source">
              <span className="launch-person">AM</span>
              <div>
                <small>Engineering</small>
                <strong>Session restore flow</strong>
                <p>Conversation · tool activity · saved changes</p>
              </div>
              <Check size={17} />
            </div>
            <div className="launch-handoff-line">
              <span />
              Shared with your teammate
              <span />
            </div>
            <div className="launch-handoff-received">
              <GitFork size={22} />
              <div>
                <small>Continue or fork</small>
                <strong>A new direction. A visible origin.</strong>
                <p>Project roles and named recipients—not IDs to paste.</p>
              </div>
            </div>
            <small className="launch-demo-label">Illustrative handoff</small>
          </div>
        </section>
        <section id="privacy" className="launch-privacy">
          <ShieldCheck size={28} strokeWidth={1.4} />
          <div>
            <span>Your device. Your boundaries.</span>
            <h2>Shared context starts with consent.</h2>
            <p>
              Choose which workspaces Nexus captures. Review detected sources,
              pause monitoring, and manage team access. Remote messages need
              your approval in Desktop; a browser cannot supply an arbitrary
              local path or command.
            </p>
            <Link to={isAuthenticated ? "/app/settings" : "/signup"}>
              Set up your workspace
              <ArrowRight size={15} />
            </Link>
          </div>
        </section>
        <section id="beta" className="launch-beta">
          <div>
            <span className="launch-beta-label">
              Early access · Windows x64 · macOS coming soon
            </span>
            <h2>
              Make room for
              <br />
              what comes next.
            </h2>
            <p>
              This is a beta: expect rough edges, help shape the workflow, and
              keep your existing agent tools. Review release notes and
              requirements before installing. macOS support is coming soon;
              a macOS build is not available yet.
            </p>
            <a
              className="launch-primary"
              href="https://github.com/Mpratyush54/nexus/releases"
              target="_blank"
              rel="noreferrer"
            >
              <Monitor size={16} />
              View desktop releases
              <ArrowRight size={15} />
            </a>
            <small>
              Download only a release explicitly marked “Pre-release”. Stable
              and beta update feeds remain separate.
            </small>
          </div>
          <div className="launch-beta-brand">
            <img
              src="/brand/nexus-mark.svg"
              alt="Nexus shared-context mark"
              width="240"
              height="240"
            />
            <span>Less starting over.</span>
          </div>
        </section>
        <section id="pricing" className="launch-pricing">
          <div className="launch-section-intro">
            <span>A little less friction</span>
            <h2>Start with the free plan.</h2>
          </div>
          <p>
            Paid upgrades are not open yet. Current plan limits come directly
            from Nexus.
          </p>
          {plans.data ? (
            <PlanGrid
              plans={plans.data}
              highlight="free"
              canChange={false}
              onPaidUnavailable={(plan) =>
                push({
                  title: "Paid upgrades are not available yet",
                  detail: `${plan.name} checkout is not open. You can keep using the free plan.`,
                  tone: "amber",
                })
              }
            />
          ) : (
            <p>
              {plans.error
                ? "Plan details are temporarily unavailable. Please try again later."
                : "Loading current plans…"}
            </p>
          )}
          <Link className="launch-secondary" to={app}>
            {isAuthenticated
              ? "Back to your workspace"
              : "Create a free account"}
            <ArrowRight size={15} />
          </Link>
        </section>
      </main>
      <footer className="launch-footer">
        <NexusBrand />
        <p>Context that moves with the work.</p>
        <div>
          <Link to="/login">Log in</Link>
          <a
            href="https://github.com/Mpratyush54/nexus"
            target="_blank"
            rel="noreferrer"
          >
            GitHub
          </a>
          <a href="#beta">Desktop beta</a>
        </div>
      </footer>
    </div>
  );
}
