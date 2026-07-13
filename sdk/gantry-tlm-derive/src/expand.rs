//! The actual code generation, only compiled when the `enabled` feature is on.

use proc_macro::TokenStream;
use proc_macro2::TokenStream as TokenStream2;
use quote::quote;
use syn::{parse_macro_input, Data, DeriveInput, Fields, LitStr, Type};

/// How a field maps onto the wire: the `Kind` variant and the `ValueWriter` call.
struct FieldPlan {
    /// On-wire field name.
    name: String,
    /// Unit string (may be empty).
    unit: String,
    /// `Kind` variant ident, e.g. `F32`.
    kind: TokenStream2,
    /// The `ValueWriter` call for this field, given `self.<ident>`.
    write: TokenStream2,
}

pub fn expand(input: TokenStream) -> TokenStream {
    let input = parse_macro_input!(input as DeriveInput);
    match expand_inner(input) {
        Ok(ts) => ts.into(),
        Err(e) => e.to_compile_error().into(),
    }
}

fn expand_inner(input: DeriveInput) -> syn::Result<TokenStream2> {
    let ident = &input.ident;

    // Packet name: struct-level #[tlm(packet = "...")] or snake_case(struct name).
    let mut packet_name = to_snake_case(&ident.to_string());
    for attr in &input.attrs {
        if attr.path().is_ident("tlm") {
            attr.parse_nested_meta(|meta| {
                if meta.path.is_ident("packet") {
                    let s: LitStr = meta.value()?.parse()?;
                    packet_name = s.value();
                    Ok(())
                } else {
                    Err(meta.error("unknown #[tlm(...)] key on struct (expected `packet`)"))
                }
            })?;
        }
    }

    let fields = match &input.data {
        Data::Struct(s) => match &s.fields {
            Fields::Named(named) => &named.named,
            _ => {
                return Err(syn::Error::new_spanned(
                    ident,
                    "#[derive(Telemetry)] requires a struct with named fields",
                ))
            }
        },
        _ => {
            return Err(syn::Error::new_spanned(
                ident,
                "#[derive(Telemetry)] can only be applied to structs",
            ))
        }
    };

    let mut plans = Vec::new();
    for field in fields {
        let fident = field.ident.as_ref().expect("named field");
        let mut name = fident.to_string();
        let mut unit = String::new();
        for attr in &field.attrs {
            if attr.path().is_ident("tlm") {
                attr.parse_nested_meta(|meta| {
                    if meta.path.is_ident("unit") {
                        let s: LitStr = meta.value()?.parse()?;
                        unit = s.value();
                        Ok(())
                    } else if meta.path.is_ident("name") {
                        let s: LitStr = meta.value()?.parse()?;
                        name = s.value();
                        Ok(())
                    } else {
                        Err(meta
                            .error("unknown #[tlm(...)] key on field (expected `unit` or `name`)"))
                    }
                })?;
            }
        }

        let (kind, write) = map_type(&field.ty, fident)?;
        plans.push(FieldPlan {
            name,
            unit,
            kind,
            write,
        });
    }

    let field_descs = plans.iter().map(|p| {
        let name = &p.name;
        let unit = &p.unit;
        let kind = &p.kind;
        quote! {
            ::gantry_tlm::FieldDesc {
                name: #name,
                kind: ::gantry_tlm::Kind::#kind,
                unit: #unit,
            }
        }
    });
    let writes = plans.iter().map(|p| &p.write);

    Ok(quote! {
        impl ::gantry_tlm::Telemetry for #ident {
            const PACKET: &'static str = #packet_name;
            const FIELDS: &'static [::gantry_tlm::FieldDesc] = &[ #(#field_descs),* ];

            fn write_values<__W: ::gantry_tlm::ValueWriter>(&self, __w: &mut __W) {
                #(#writes)*
            }

            fn id_cell() -> &'static ::core::sync::atomic::AtomicU32 {
                static ID: ::core::sync::atomic::AtomicU32 =
                    ::core::sync::atomic::AtomicU32::new(0);
                &ID
            }
        }
    })
}

/// Map a supported field type to `(Kind variant, ValueWriter call)`.
fn map_type(ty: &Type, fident: &syn::Ident) -> syn::Result<(TokenStream2, TokenStream2)> {
    let ident = match ty {
        Type::Path(p) if p.qself.is_none() => match p.path.get_ident() {
            Some(id) => id.to_string(),
            None => return Err(unsupported(ty)),
        },
        _ => return Err(unsupported(ty)),
    };

    let (kind, write) = match ident.as_str() {
        "f32" => (quote!(F32), quote! { __w.field_f32(self.#fident); }),
        "f64" => (quote!(F64), quote! { __w.field_f64(self.#fident); }),
        "bool" => (quote!(Bool), quote! { __w.field_bool(self.#fident); }),
        // Integers widen to i64 on the wire.
        "i8" | "i16" | "i32" | "i64" | "u8" | "u16" | "u32" => {
            (quote!(I64), quote! { __w.field_i64(self.#fident as i64); })
        }
        _ => return Err(unsupported(ty)),
    };
    Ok((kind, write))
}

fn unsupported(ty: &Type) -> syn::Error {
    syn::Error::new_spanned(
        ty,
        "unsupported #[derive(Telemetry)] field type: expected one of \
         f32, f64, bool, i8, i16, i32, i64, u8, u16, u32 \
         (&str and other types are not supported in v1)",
    )
}

/// Convert a struct name like `ImuState` to `imu_state`.
fn to_snake_case(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 4);
    for (i, ch) in s.chars().enumerate() {
        if ch.is_ascii_uppercase() {
            if i != 0 {
                out.push('_');
            }
            out.push(ch.to_ascii_lowercase());
        } else {
            out.push(ch);
        }
    }
    out
}
