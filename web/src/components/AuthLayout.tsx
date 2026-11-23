import type { ReactNode } from "react";

// The sign-in look of the cactus family: a green panel saying what the app is, the form on paper.
export function AuthLayout({ children, version }: { children: ReactNode; version?: string }) {
  return (
    <div className="auth">
      <section className="auth-story" aria-label="About cactusTrade">
        <h1>Your share trading, run by rules you set</h1>
        <p>
          cactusTrade watches London and New York shares, reads the market's regime, and trades real shares on your eToro
          account — long only, never leveraged, inside a trading budget, a daily loss limit and a kill switch. It starts
          on demo money; real money needs a deliberate, gated switch.
        </p>
        <div className="chips">
          {["Real shares, long only", "Leverage 1, always", "Demo first", "Risk engine & kill switch", "LLM reviewer can only say no", "Every decision logged"].map((c) => (
            <span className="chip" key={c}>{c}</span>
          ))}
        </div>
        <p className="mt-4 mb-0 small">
          Free and open source: <a href="https://github.com/albievan/cactustrade" target="_blank" rel="noreferrer"><i className="bi bi-github me-1" />github.com/albievan/cactustrade</a>
        </p>
        <p className="small mb-0" style={{ opacity: 0.75 }}>Software, not financial advice. Trading can lose money.</p>
        <svg className="waves" viewBox="0 0 800 200" preserveAspectRatio="none" aria-hidden="true">
          {Array.from({ length: 14 }, (_, i) => (
            <path key={i} d={`M0 ${120 + i * 5} C 200 ${60 + i * 8}, 500 ${190 - i * 4}, 800 ${100 + i * 6}`} fill="none" stroke="#8fcbb0" strokeWidth="1" />
          ))}
        </svg>
      </section>
      <section className="auth-form">
        <div className="inner">
          <div className="brand">
            <img src="/logo.svg" alt="" />
            <div>
              <div className="name">cactus<b>trade</b></div>
              <div className="tag">Autonomous share trader</div>
            </div>
          </div>
          {children}
        </div>
        <div className="foot">
          {version && <span className="text-secondary me-3">v{version}</span>}
          <a href="https://github.com/albievan/cactustrade" target="_blank" rel="noreferrer"><i className="bi bi-github me-1" />Source on GitHub</a>
        </div>
      </section>
    </div>
  );
}
