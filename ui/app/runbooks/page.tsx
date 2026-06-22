"use client";

export default function RunbooksPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Runbooks (Past Incidents)</h1>
        <p className="text-xs text-[var(--color-text-dim)] mt-1">
          Past incidents are stored in Weaviate. Future incidents retrieve the most similar
          ones with code-churn decay applied to relevance scores.
        </p>
      </div>
      <div className="panel">
        <div className="panel-body text-center text-sm text-[var(--color-text-dim)] py-12">
          Connect to Weaviate to view stored runbooks.
        </div>
      </div>
    </div>
  );
}
