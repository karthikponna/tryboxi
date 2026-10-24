"use client";

import * as React from "react";
import { Accordion as AccordionPrimitive } from "radix-ui";
import { cn } from "@/lib/utils";

function Accordion({ className, ...props }: React.ComponentProps<typeof AccordionPrimitive.Root>) {
  return (
    <AccordionPrimitive.Root
      data-slot="accordion"
      className={cn("flex w-full flex-col border-t border-line", className)}
      {...props}
    />
  );
}

function AccordionItem({ className, ...props }: React.ComponentProps<typeof AccordionPrimitive.Item>) {
  return (
    <AccordionPrimitive.Item
      data-slot="accordion-item"
      className={cn("border-b border-line", className)}
      {...props}
    />
  );
}

function AccordionTrigger({
  className,
  children,
  ...props
}: React.ComponentProps<typeof AccordionPrimitive.Trigger>) {
  return (
    <AccordionPrimitive.Header className="flex">
      <AccordionPrimitive.Trigger
        data-slot="accordion-trigger"
        className={cn(
          "group/acc flex flex-1 items-center justify-between gap-6 py-5 text-left font-display text-[19px] leading-[1.3] tracking-[-0.015em] text-fg transition-colors duration-150 outline-none hover:text-fg lg:text-[22px]",
          "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green",
          className,
        )}
        {...props}
      >
        {children}
        <span
          aria-hidden="true"
          className="w-4 shrink-0 text-center font-mono text-[15px] text-fg-dim transition-colors group-hover/acc:text-fg group-data-[state=open]/acc:text-green"
        >
          <span className="group-data-[state=open]/acc:hidden">+</span>
          <span className="hidden group-data-[state=open]/acc:inline">−</span>
        </span>
      </AccordionPrimitive.Trigger>
    </AccordionPrimitive.Header>
  );
}

function AccordionContent({
  className,
  children,
  ...props
}: React.ComponentProps<typeof AccordionPrimitive.Content>) {
  return (
    <AccordionPrimitive.Content
      data-slot="accordion-content"
      className="overflow-hidden data-[state=closed]:animate-accordion-up data-[state=open]:animate-accordion-down"
      {...props}
    >
      <div className={cn("max-w-[640px] pb-6 text-[16px] leading-[1.6] text-fg-muted", className)}>
        {children}
      </div>
    </AccordionPrimitive.Content>
  );
}

export { Accordion, AccordionItem, AccordionTrigger, AccordionContent };
