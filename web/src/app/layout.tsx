import type { Metadata } from "next";
import { Bricolage_Grotesque, Inter, JetBrains_Mono } from "next/font/google";
import "./globals.css";
import { Providers } from "./providers";
import Link from "next/link";
import Image from "next/image";
import { Sidebar } from "@/components/Sidebar";
import { ProfileMenu } from "@/components/ProfileMenu";

// Design system spec's Google Fonts loading strategy ("media=print swap,
// non-blocking FCP") is a raw-<link> technique for template-rendered HTML.
// next/font/google achieves the same non-blocking-render goal natively --
// it self-hosts the font files at build time and injects the @font-face
// with font-display: swap, so there's no FOIT and no extra network hop to
// fonts.googleapis.com at request time. This is the framework-idiomatic
// equivalent, not a downgrade from the spec's intent.
const fontDisplay = Bricolage_Grotesque({
  variable: "--font-display",
  subsets: ["latin"],
  weight: ["600", "700", "800"],
});

const fontBody = Inter({
  variable: "--font-body",
  subsets: ["latin"],
  weight: ["400", "500", "600"],
});

const fontMono = JetBrains_Mono({
  variable: "--font-mono",
  subsets: ["latin"],
  weight: ["400", "500"],
});

export const metadata: Metadata = {
  title: "Practice Engine",
  description: "Practice Engine — Guided Labs, Simulations, and Projects",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`${fontDisplay.variable} ${fontBody.variable} ${fontMono.variable} h-full antialiased`}
      style={{ colorScheme: "light" }}
    >
      <body className="min-h-full flex flex-col">
        <Providers>
          <header className="topnav">
            <nav className="topnav-inner">
              <Link href="/" className="brand">
                <Image
                  src="/assets/skillfyme-logo.png"
                  alt="Skillfyme"
                  width={140}
                  height={28}
                  priority
                  style={{ height: "24px", width: "auto" }}
                />
              </Link>
              <div className="topnav-spacer" />
              <ProfileMenu />
            </nav>
          </header>
          <div className="app-shell">
            <Sidebar />
            <main className="flex-1">{children}</main>
          </div>
        </Providers>
      </body>
    </html>
  );
}
