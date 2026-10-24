import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { Slot } from "radix-ui";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "group/button inline-flex shrink-0 items-center justify-center gap-2 rounded-[6px] text-[14px] leading-none font-medium whitespace-nowrap select-none disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        primary:
          "bg-green text-green-ink transition-[background-color,transform] duration-150 hover:bg-green-hover active:scale-[0.98] active:bg-green-press",
        secondary:
          "border border-line-strong text-fg transition-[background-color,border-color,transform] duration-150 hover:border-fg/30 hover:bg-panel-2 active:scale-[0.98]",
        ghost:
          "text-fg-muted transition-colors duration-150 hover:bg-panel-2 hover:text-fg active:scale-[0.98]",
      },
      size: {
        default: "h-10 px-4",
        sm: "h-9 px-3",
      },
    },
    defaultVariants: {
      variant: "primary",
      size: "default",
    },
  },
);

function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  }) {
  const Comp = asChild ? Slot.Root : "button";
  return (
    <Comp
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  );
}

function Arrow() {
  return (
    <span
      aria-hidden="true"
      className="inline-block transition-transform duration-150 ease-(--bx-ease-out) group-hover/button:translate-x-0.5"
    >
      →
    </span>
  );
}

export { Arrow, Button, buttonVariants };
