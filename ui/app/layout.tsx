import type { Metadata } from "next";
import "./globals.css";
import { Sidebar } from "@/components/sidebar";
import { TopBar } from "@/components/topbar";
import { Providers } from "@/components/providers";

export const metadata: Metadata = {
  title: "Sentinel — Autonomous AI SRE",
  description: "Detect, diagnose, fix, verify — autonomously.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        <Providers>
          <div className="flex min-h-screen">
            <Sidebar />
            <div className="flex-1 flex flex-col min-w-0">
              <TopBar />
              <main className="flex-1 p-6 overflow-x-auto">{children}</main>
            </div>
          </div>
        </Providers>
      </body>
    </html>
  );
}
