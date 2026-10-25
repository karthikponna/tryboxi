import { site } from "@/site.config";

// schema.org JSON-LD so search engines can show tryboxi as a free macOS developer app.
const data = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "WebSite",
      "@id": `${site.url}/#website`,
      url: site.url,
      name: site.name,
      description: site.description,
      inLanguage: "en",
    },
    {
      "@type": "SoftwareApplication",
      "@id": `${site.url}/#app`,
      name: site.name,
      url: site.url,
      description: site.description,
      applicationCategory: "DeveloperApplication",
      operatingSystem: "macOS 14 or later (Apple Silicon)",
      offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
      license: "https://opensource.org/licenses/MIT",
      image: `${site.url}/opengraph-image`,
      author: { "@type": "Person", name: site.author, url: site.social.x },
      sameAs: [site.repo.url, site.social.x],
    },
    {
      "@type": "SoftwareSourceCode",
      "@id": `${site.url}/#source`,
      name: site.name,
      codeRepository: site.repo.url,
      programmingLanguage: ["Go", "TypeScript"],
      license: "https://opensource.org/licenses/MIT",
      targetProduct: { "@id": `${site.url}/#app` },
    },
  ],
};

export function StructuredData() {
  return (
    <script
      type="application/ld+json"
      // static, trusted content; `<` is escaped so the JSON can never close the tag
      dangerouslySetInnerHTML={{ __html: JSON.stringify(data).replace(/</g, "\\u003c") }}
    />
  );
}
