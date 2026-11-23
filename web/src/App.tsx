import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { useSession } from "./lib/session";
import { Login } from "./pages/Login";
import { ChangeDefaultPassword } from "./pages/ChangeDefaultPassword";
import { Dashboard } from "./pages/Dashboard";
import { Credentials } from "./pages/Credentials";
import { Users } from "./pages/Users";
import { ApiKeys } from "./pages/ApiKeys";
import { Audit } from "./pages/Audit";
import { Watchlist } from "./pages/Watchlist";
import { Account } from "./pages/Account";

export function App() {
  const { me, loading } = useSession();
  if (loading) return <div className="p-5 text-center text-secondary">Loading…</div>;
  if (!me) return <Login />;
  // The built-in admin/admin must choose a new password before anything else.
  if (me.user.mustChangePassword) return <ChangeDefaultPassword />;
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="watchlist" element={<Watchlist />} />
        <Route path="credentials" element={<Credentials />} />
        <Route path="users" element={<Users />} />
        <Route path="api-keys" element={<ApiKeys />} />
        <Route path="audit" element={<Audit />} />
        <Route path="account" element={<Account />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
