local
a=
{
b=
1
,
}
a
.c
=
function(self,d)
return
self
.b
+
d
end
return
a
:c(
1
)
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__