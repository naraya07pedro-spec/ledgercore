import type { Metadata } from "next";
import "./styles.css";

export const metadata: Metadata = {
  title: "LedgerCore | Operations",
  description: "Synthetic financial ledger operations",
};

export default function Layout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="en"><body>{children}</body></html>;
}
