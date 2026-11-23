import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { useSession } from "./lib/session";
import { Login } from "./pages/Login";
import { ChangeDefaultPassword } from "./pages/ChangeDefaultPassword";
import { Dashboard } from "./pages/Dashboard";
import { Users } from "./pages/Users";
import { ApiKeys } from "./pages/ApiKeys";
import { Audit } from "./pages/Audit";
import { Watchlist } from "./pages/Watchlist";
import { Accounts } from "./pages/Accounts";
import { Themes } from "./pages/Themes";
import { System } from "./pages/System";
import { RiskLimits } from "./pages/RiskLimits";
import { LlmReviewer } from "./pages/LlmReviewer";
import { ModeProvider } from "./lib/mode";
import { Account } from "./pages/Account";

export function App() {
  const { me, loading } = useSession();
  if (loading) return <div className="p-5 text-center text-secondary">Loading…</div>;
  if (!me) return <Login />;
  // The built-in admin/admin must choose a new password before anything else.
  if (me.user.mustChangePassword) return <ChangeDefaultPassword />;
  return (
    <ModeProvider>
    <Routes>
      <Route element={<Layout />}>
        <Route path="risk" element={<RiskLimits />} />
        <Route path="llm" element={<LlmReviewer />} />
        <Route path="system" element={<System />} />
        <Route index element={<Dashboard />} />
        <Route path="watchlist" element={<Watchlist />} />
        <Route path="accounts" element={<Accounts />} />
        <Route path="themes" element={<Themes />} />
        <Route path="credentials" element={<Navigate to="/accounts" replace />} />
        <Route path="users" element={<Users />} />
        <Route path="api-keys" element={<ApiKeys />} />
        <Route path="audit" element={<Audit />} />
        <Route path="account" element={<Account />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
    </ModeProvider>
  );
}
