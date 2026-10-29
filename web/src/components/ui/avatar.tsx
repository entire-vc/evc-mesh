import { cn } from "@/lib/cn";

interface AvatarProps {
  src?: string;
  name: string;
  size?: "sm" | "md" | "lg";
  className?: string;
}

const sizeClasses = {
  sm: "h-6 w-6 text-xs",
  md: "h-8 w-8 text-sm",
  lg: "h-10 w-10 text-base",
};

// First letter or digit of each word, so "Hana (QA Mesh)" gives "HQ", not "H(".
function getInitials(name: string): string {
  return name
    .split(/\s+/)
    .map((part) => part.match(/[\p{L}\p{N}]/u)?.[0])
    .filter(Boolean)
    .slice(0, 2)
    .join("")
    .toUpperCase();
}

export function Avatar({ src, name, size = "md", className }: AvatarProps) {
  if (src) {
    return (
      <img
        src={src}
        alt={name}
        className={cn(
          "inline-flex shrink-0 rounded-full object-cover",
          sizeClasses[size],
          className,
        )}
      />
    );
  }

  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-full bg-primary font-medium text-primary-foreground",
        sizeClasses[size],
        className,
      )}
    >
      {getInitials(name)}
    </span>
  );
}
