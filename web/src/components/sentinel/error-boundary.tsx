"use client";

import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
  sectionName?: string;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

/**
 * ErrorBoundary — catches render errors in its children and shows a fallback
 * UI instead of white-screening the entire app.
 *
 * Without this, any render throw (e.g. `incident.beta_alpha.toFixed(2)` on
 * undefined) crashes the whole dashboard. With it, only the affected section
 * shows an error, and the rest of the app keeps working.
 */
export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    // Log to console for debugging — in production you'd send to Sentry/etc.
    console.error(
      `[ErrorBoundary${this.props.sectionName ? `: ${this.props.sectionName}` : ""}]`,
      error,
      errorInfo,
    );
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }
      return (
        <div className="flex min-h-[200px] flex-col items-center justify-center gap-3 rounded-lg border border-rose-500/30 bg-rose-500/5 p-6 text-center">
          <div className="text-2xl">⚠️</div>
          <div className="text-sm font-medium text-rose-300">
            {this.props.sectionName
              ? `${this.props.sectionName} failed to render`
              : "Something went wrong"}
          </div>
          <div className="max-w-md text-xs text-zinc-500">
            {this.state.error?.message || "An unexpected error occurred."}
          </div>
          <button
            type="button"
            onClick={() => this.setState({ hasError: false, error: null })}
            className="mt-2 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            Try again
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
