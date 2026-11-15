import { useEffect, useState } from "react";

type CheckResult = { status: "up" | "down"; required: boolean };
type Readiness = { status: string; checks: Record<string, CheckResult> };
type State =
  | { kind: "loading" }
  | { kind: "ok"; data: Readiness }
  | { kind: "unreachable" };

// Phase 1 placeholder: shows backend readiness so the dev stack can be verified end to end.
export default function App() {
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/readyz", { signal: controller.signal })
      .then(async (res) => (await res.json()) as Readiness)
      .then((data) => setState({ kind: "ok", data }))
      .catch((err: unknown) => {
        if (err instanceof DOMException && err.name === "AbortError") return;
        setState({ kind: "unreachable" });
      });
    return () => controller.abort();
  }, []);

  return (
    <main className="mx-auto max-w-xl p-8 font-sans">
      <h1 className="text-2xl font-semibold">Real-Time Chat &amp; Messaging Platform</h1>
      <p className="mt-1 text-sm text-gray-500">Phase 1 foundation: backend status</p>

      <section className="mt-6 rounded border border-gray-200 p-4">
        {state.kind === "loading" && <p>Checking backend…</p>}
        {state.kind === "unreachable" && <p className="text-red-600">Backend unreachable.</p>}
        {state.kind === "ok" && (
          <>
            <p className="font-medium">Overall: {state.data.status}</p>
            <ul className="mt-2 space-y-1 text-sm">
              {Object.entries(state.data.checks).map(([name, c]) => (
                <li key={name}>
                  {name}: <span className={c.status === "up" ? "text-green-700" : "text-red-600"}>{c.status}</span>
                  {!c.required && <span className="text-gray-400"> (optional)</span>}
                </li>
              ))}
            </ul>
          </>
        )}
      </section>
    </main>
  );
}
